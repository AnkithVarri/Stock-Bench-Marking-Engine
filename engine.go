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

