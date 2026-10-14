import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"syscall"
	"time"
)

const (
	naiveSoftCap = 100_000
	naiveHardCap = 2_000_000
	histBins     = 512
)

type Mode int

const (
	ModeSequential Mode = iota
	ModeNaive
	ModePool
	ModePartition
	ModeSharedRNG
	ModeLockedAgg
)

func (m Mode) String() string {
	switch m {
	case ModeSequential:
		return "sequential"
	case ModeNaive:
		return "naive"
	case ModePool:
		return "pool"
	case ModePartition:
		return "partition"
	case ModeSharedRNG:
		return "shared-rng"
	case ModeLockedAgg:
		return "locked-agg"
	default:
		return "?"
	}
}

func ParseMode(s string) (Mode, error) {
	switch s {
	case "sequential":
		return ModeSequential, nil
	case "naive":
		return ModeNaive, nil
	case "pool":
		return ModePool, nil
	case "partition":
		return ModePartition, nil
	case "shared", "shared-rng":
		return ModeSharedRNG, nil
	case "locked", "locked-agg":
		return ModeLockedAgg, nil
	default:
		return 0, fmt.Errorf("mode must be sequential, naive, pool, partition, shared-rng, or locked-agg")
	}
}

type Result struct {
	Mode        Mode
	Product     Product
	Workers     int
	Batch       int
	Paths       int
	Elapsed     time.Duration
	PathsPerSec float64
	Cores       float64
	Mallocs     uint64
	AllocBytes  uint64
	Mean        float64
	StdErr      float64
	VaR95       float64
	ES95        float64
	VaR99       float64
	ES99        float64
	Retained    int
	Closed      bool
}


func Run(p Params) (Result, error) {
	if err := p.validate(); err != nil {
		return Result{}, err
	}
	pr, err := prepare(p)
	if err != nil {
		return Result{}, err
	}
	if pr.hasClosed {
		return closedResult(p, pr.closed), nil
	}

	var mem0 runtime.MemStats
	runtime.ReadMemStats(&mem0)
	cpu0 := cpuNow()

	var res Result
	switch p.Mode {
	case ModeSequential:
		res = runSequential(p, &pr)
	case ModeNaive:
		res = runNaive(p, &pr)
	case ModePool:
		res = runPool(p, &pr)
	case ModePartition:
		res = runPartition(p, &pr, partLocal)
	case ModeSharedRNG:
		res = runPartition(p, &pr, partShared)
	case ModeLockedAgg:
		res = runPartition(p, &pr, partLocked)
	default:
		return Result{}, fmt.Errorf("unknown mode %s", p.Mode)
	}

	elapsed := res.Elapsed
	if elapsed > 0 {
		res.Cores = float64(cpuNow()-cpu0) / float64(elapsed)
		res.PathsPerSec = float64(res.Paths) / elapsed.Seconds()
	}
	var mem1 runtime.MemStats
	runtime.ReadMemStats(&mem1)
	res.Mallocs = mem1.Mallocs - mem0.Mallocs
	res.AllocBytes = mem1.TotalAlloc - mem0.TotalAlloc
	res.Product = p.Product
	return res, nil
}

func closedResult(p Params, v float64) Result {
	res := Result{
		Mode: p.Mode, Product: p.Product, Paths: p.Paths, Mean: v,
		Workers: 1, Closed: true, Retained: 32,
	}
	if p.Product.needsHist() {
		res.VaR95, res.VaR99 = v, v
		res.ES95, res.ES99 = v, v
	}
	return res
}

func finish(p Params, a acc, workers, batch int, elapsed time.Duration, mode Mode) Result {
	mean, stderr := a.m.meanStd()
	res := Result{
		Mode: mode, Workers: workers, Batch: batch, Paths: p.Paths,
		Elapsed: elapsed, Mean: mean, StdErr: stderr, Retained: a.retained(),
	}
	if a.h != nil {
		res.VaR95 = a.h.quantile(0.95)
		res.ES95 = a.h.es(0.95)
		res.VaR99 = a.h.quantile(0.99)
		res.ES99 = a.h.es(0.99)
	}
	return res
}

func newAcc(pr *prepared) acc {
	a := acc{}
	if pr.product.needsHist() {
		a.h = makeHist(pr.histLo, pr.histHi, histBins)
	}
	return a
}

func runSequential(p Params, pr *prepared) Result {
	rng := newRNG(p.Seed, 0)
	a := newAcc(pr)
	start := time.Now()
	consume(pr, rng, p.Paths, &a)
	return finish(p, a, 1, 0, time.Since(start), ModeSequential)
}

func runNaive(p Params, pr *prepared) Result {
	const shards = 64
	sh := make([]shard, shards)
	for i := range sh {
		sh[i].a = newAcc(pr)
	}
	var wg sync.WaitGroup
	wg.Add(p.Paths)
	start := time.Now()
	for i := 0; i < p.Paths; i++ {
		go func(i int) {
			defer wg.Done()
			rng := newRNG(p.Seed, i)
			x := onePayoff(pr, rng)
			s := &sh[i&(shards-1)]
			s.mu.Lock()
			s.a.observe(x)
			s.mu.Unlock()
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	merged := newAcc(pr)
	for i := range sh {
		merged.merge(sh[i].a)
	}
	return finish(p, merged, p.Paths, 0, elapsed, ModeNaive)
}


type shard struct {
	mu sync.Mutex
	a  acc
	_  [64]byte // keep neighbouring shard locks off one cache line
}

func runPool(p Params, pr *prepared) Result {
	workers := workerCount(p)
	batch := p.Batch
	if batch < 1 {
		batch = 8192
	}
	jobs := make(chan int, workers) // batch 1 means one send per path
	parts := make([]acc, workers)
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		parts[w] = newAcc(pr)
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := newRNG(p.Seed, w)
			dst := &parts[w]
			for n := range jobs {
				consume(pr, rng, n, dst)
			}
		}(w)
	}
	left := p.Paths
	for left > 0 {
		n := batch
		if n > left {
			n = left
		}
		jobs <- n
		left -= n
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(start)
	merged := newAcc(pr)
	for i := range parts {
		merged.merge(parts[i])
	}
	return finish(p, merged, workers, batch, elapsed, ModePool)
}

type partKind int

const (
	partLocal partKind = iota
	partShared
	partLocked
)

func runPartition(p Params, pr *prepared, kind partKind) Result {
	workers := workerCount(p)
	counts := splitPaths(p.Paths, workers)
	parts := make([]acc, workers)
	for i := range parts {
		parts[i] = newAcc(pr)
	}
	mode := ModePartition
	var shared *lockedRNG
	var lock sync.Mutex
	sharedAcc := newAcc(pr)
	if kind == partShared {
		mode = ModeSharedRNG
		shared = &lockedRNG{r: newRNG(p.Seed, 0)}
	}
	if kind == partLocked {
		mode = ModeLockedAgg
	}

	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		if counts[w] == 0 {
			continue
		}
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			switch kind {
			case partShared:
				consume(pr, shared, counts[w], &parts[w])
			case partLocked:
				rng := newRNG(p.Seed, w)
				for i := 0; i < counts[w]; i++ {
					x := onePayoff(pr, rng)
					lock.Lock()
					sharedAcc.observe(x)
					lock.Unlock()
				}
			default:
				rng := newRNG(p.Seed, w)
				consume(pr, rng, counts[w], &parts[w])
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	if kind == partLocked {
		return finish(p, sharedAcc, workers, 0, elapsed, mode)
	}
	merged := newAcc(pr)
	for i := range parts {
		merged.merge(parts[i])
	}
	return finish(p, merged, workers, 0, elapsed, mode)
}

func onePayoff[R interface{ NormFloat64() float64 }](pr *prepared, rng R) float64 {
	var sink acc
	consume(pr, rng, 1, &sink)
	if sink.m.n == 0 {
		return 0
	}
	return sink.m.sum
}

func workerCount(p Params) int {
	n := p.Workers
	if n < 1 {
		n = runtime.NumCPU()
	}
	if n > p.Paths {
		n = p.Paths
	}
	return n
}

func splitPaths(paths, workers int) []int {
	counts := make([]int, workers)
	base := paths / workers
	extra := paths % workers
	for i := 0; i < workers; i++ {
		counts[i] = base
		if i < extra {
			counts[i]++
		}
	}
	return counts
}

type lockedRNG struct {
	mu sync.Mutex
	r  *rand.Rand
}

func (g *lockedRNG) NormFloat64() float64 {
	g.mu.Lock()
	z := g.r.NormFloat64()
	g.mu.Unlock()
	return z
}

func newRNG(seed int64, stream int) *rand.Rand {
	s1, s2 := streamSeed(seed, stream)
	return rand.New(rand.NewPCG(s1, s2))
}


func streamSeed(base int64, stream int) (uint64, uint64) {
	x := uint64(base) ^ 0xA0761D6478BD642F
	x += uint64(stream) * 0x9E3779B97F4A7C15
	return splitmix64(&x), splitmix64(&x)
}

func splitmix64(x *uint64) uint64 {
	*x += 0x9E3779B97F4A7C15
	z := *x
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func cpuNow() time.Duration {
	var r syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &r) != nil {
		return 0
	}
	return time.Duration(r.Utime.Nano()) + time.Duration(r.Stime.Nano())
}


