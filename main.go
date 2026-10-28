package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"
)

func main() {
	p, bench, cpuProfile, memProfile, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if err := startProfile(cpuProfile); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer stopProfile()

	if bench {
		if err := runBench(p); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	} else if err := runOnce(p); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := writeHeap(memProfile); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}


func parseFlags() (Params, bool, string, string, error) {
	productStr := flag.String("product", "european", "gbm, european, asian, barrier, var, portfolio")
	modeStr := flag.String("mode", "partition", "sequential, naive, pool, partition, shared-rng, locked-agg")
	spot := flag.Float64("spot", 100, "spot")
	strike := flag.Float64("strike", 100, "strike")
	rate := flag.Float64("rate", 0.05, "risk-free rate, continuous")
	drift := flag.Float64("drift", 0, "real drift; defaults to -rate when omitted")
	vol := flag.Float64("vol", 0.2, "volatility")
	years := flag.Float64("years", 1, "horizon in years")
	barrier := flag.Float64("barrier", 80, "down-and-out barrier")
	steps := flag.Int("steps", 32, "fixings for asian and barrier")
	paths := flag.Int("paths", 2_000_000, "simulated paths")
	workers := flag.Int("workers", 0, "workers (0 = one per CPU)")
	batch := flag.Int("batch", 8192, "paths per channel send, pool mode only")
	seed := flag.Int64("seed", 1, "rng seed")
	force := flag.Bool("force", false, "allow naive mode above 100k paths")
	spots := flag.String("spots", "", "portfolio spots, comma-separated")
	vols := flag.String("vols", "", "portfolio vols, comma-separated")
	qty := flag.String("qty", "", "portfolio quantities, comma-separated")
	corr := flag.String("corr", "", "portfolio correlation, row-major")
	bench := flag.Bool("bench", false, "print the scheduler comparison")
	profile := flag.Bool("profile", false, "write cpu.prof and mem.prof")
	cpuProfile := flag.String("cpuprofile", "", "cpu profile path")
	memProfile := flag.String("memprofile", "", "heap profile path")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `mcengine — parallel Monte Carlo, text only.

  go run .                         2M European paths, static partition
  go run . -bench                  scheduler table, then a 100M one-step run
  go run . -product portfolio -paths 1000000
  go run . -mode pool -batch 1
  go run . -profile -paths 5000000
  go tool pprof -top cpu.prof

`)
		flag.PrintDefaults()
	}
	flag.Parse()

	product, err := ParseProduct(*productStr)
	if err != nil {
		return Params{}, false, "", "", err
	}
	mode, err := ParseMode(*modeStr)
	if err != nil {
		return Params{}, false, "", "", err
	}
	driftSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "drift" {
			driftSet = true
		}
	})
	p := Params{
		Product: product, Mode: mode,
		Spot: *spot, Strike: *strike, Rate: *rate, Drift: *drift, DriftSet: driftSet,
		Vol: *vol, Years: *years, Barrier: *barrier, Steps: *steps,
		Paths: *paths, Workers: *workers, Batch: *batch, Seed: *seed, Force: *force,
	}
	if product == ProductPortfolio {
		sp, err := parseFloats(*spots)
		if err != nil {
			return Params{}, false, "", "", err
		}
		vs, err := parseFloats(*vols)
		if err != nil {
			return Params{}, false, "", "", err
		}
		qs, err := parseFloats(*qty)
		if err != nil {
			return Params{}, false, "", "", err
		}
		cr, err := parseFloats(*corr)
		if err != nil {
			return Params{}, false, "", "", err
		}
		assets, err := buildAssets(sp, vs, qs)
		if err != nil {
			return Params{}, false, "", "", err
		}
		p.Assets = assets
		p.Corr = cr
	}
	cpu, mem := *cpuProfile, *memProfile
	if *profile {
		if cpu == "" {
			cpu = "cpu.prof"
		}
		if mem == "" {
			mem = "mem.prof"
		}
	}
	return p, *bench, cpu, mem, nil
}


func runOnce(p Params) error {
	res, err := Run(p)
	if err != nil {
		return err
	}
	printRun(p, res)
	return nil
}


func printRun(p Params, res Result) {
	fmt.Printf("Monte Carlo  %s  %s\n\n", p.Product, res.Mode)
	fmt.Printf("  %-12s %s\n", "paths", comma(p.Paths))
	fmt.Printf("  %-12s %d\n", "workers", res.Workers)
	if res.Batch > 0 {
		fmt.Printf("  %-12s %s\n", "batch", comma(res.Batch))
	}
	fmt.Printf("  %-12s %s\n", "horizon", fmt.Sprintf("%.2f years", p.Years))
	fmt.Printf("  %-12s %s\n", "drift", fmt.Sprintf("%.2f%%", p.mu()*100))
	if p.Product == ProductPortfolio {
		fmt.Printf("  %-12s %d assets\n", "book", len(p.Assets))
	} else {
		fmt.Printf("  %-12s %.4g @ %.2f%% vol\n", "spot", p.Spot, p.Vol*100)
	}
	fmt.Println()
	fmt.Printf("  %-12s %s\n", "mean", num(res.Mean))
	fmt.Printf("  %-12s %s\n", "std. error", num(res.StdErr))
	if p.Product.needsHist() {
		fmt.Printf("  %-12s %s    ES %s\n", "VaR 95", num(res.VaR95), num(res.ES95))
		fmt.Printf("  %-12s %s    ES %s\n", "VaR 99", num(res.VaR99), num(res.ES99))
		if res.Mean < 0 {
			fmt.Println("  negative mean loss means the book gained on average")
		}
	}
	if ref, ok := reference(p); ok && !res.Closed {
		fmt.Printf("  %-12s %s\n", "analytic", num(ref))
		fmt.Printf("  %-12s %+.6f\n", "difference", res.Mean-ref)
	}
	if res.Closed {
		fmt.Printf("  %-12s deterministic, nothing was simulated\n", "note")
	}
	fmt.Println()
	fmt.Printf("  %-12s %s\n", "elapsed", dur(res.Elapsed))
	fmt.Printf("  %-12s %s paths/sec\n", "throughput", rate(res.PathsPerSec))
	fmt.Printf("  %-12s %.2f cores\n", "cpu", res.Cores)
	fmt.Printf("  %-12s %s\n", "allocs", comma64(res.Mallocs))
	fmt.Printf("  %-12s %s  (a payoff slice would be %s)\n", "retained", bytes(res.Retained), bytes(p.Paths*8))
	fmt.Println()
	if ref, ok := reference(p); ok && res.StdErr > 0 {
		d := math.Abs(res.Mean - ref)
		if d <= 2*res.StdErr {
			fmt.Println("Within 2 standard errors of the closed form.")
		} else {
			fmt.Printf("About %.1f standard errors from the closed form.\n", d/res.StdErr)
		}
	}
	fmt.Println("Scheduler comparison: go run . -bench")
}


func runBench(p Params) error {
	fmt.Printf("Monte Carlo bench  %s\n", p.Product)
	fmt.Printf("paths %s   seed %d   CPUs %d\n\n", comma(p.Paths), p.Seed, runtime.NumCPU())

	warm := p
	warm.Paths = min(p.Paths, 50_000)
	warm.Workers = 2
	warm.Mode = ModePartition
	warm.Force = false
	if _, err := Run(warm); err != nil {
		return err
	}

	fmt.Println("Static partition — one block per worker, private RNG, totals merged at the end")
	fmt.Println()
	header()
	var scale []Result
	for _, w := range []int{1, 2, 4, 8, 16, 32} {
		run := p
		run.Mode = ModePartition
		run.Workers = w
		res, err := Run(run)
		if err != nil {
			return err
		}
		scale = append(scale, res)
		printRow(res)
	}

	fmt.Println()
	fmt.Println("Same path count, other schedulers (naive is capped — one goroutine per path)")
	fmt.Println()
	header()
	costs := map[string]Result{}
	focus := 8
	seq := p
	seq.Mode = ModeSequential
	seq.Workers = 1
	if err := put(costs, "sequential", seq); err != nil {
		return err
	}

	naive := p
	naive.Mode = ModeNaive
	naive.Paths = min(p.Paths, naiveSoftCap)
	naive.Force = false
	if err := put(costs, "naive", naive); err != nil {
		return err
	}

	for _, batch := range []int{1, 256, 8192} {
		run := p
		run.Mode = ModePool
		run.Workers = focus
		run.Batch = batch
		if err := put(costs, fmt.Sprintf("pool/%d", batch), run); err != nil {
			return err
		}
	}
	for _, mode := range []Mode{ModePartition, ModeSharedRNG, ModeLockedAgg} {
		run := p
		run.Mode = mode
		run.Workers = focus
		if err := put(costs, mode.String(), run); err != nil {
			return err
		}
	}

	var big Result
	var bigErr error
	if p.Product.oneStep() {
		fmt.Println()
		fmt.Println("100M paths, static partition, streaming totals")
		fmt.Println()
		header()
		run := p
		run.Mode = ModePartition
		run.Workers = 0
		run.Paths = 100_000_000
		big, bigErr = Run(run)
		if bigErr != nil {
			return bigErr
		}
		printRow(big)
	}

	fmt.Println()
	explain(scale, costs, big, p.Product.oneStep())
	return nil
}

func put(dst map[string]Result, key string, p Params) error {
	res, err := Run(p)
	if err != nil {
		return err
	}
	dst[key] = res
	printRow(res)
	return nil
}

func explain(scale []Result, costs map[string]Result, big Result, oneStep bool) {
	r1, ok1 := byWorkers(scale, 1)
	r8, ok8 := byWorkers(scale, 8)
	r32, ok32 := byWorkers(scale, 32)
	if ok1 && ok8 && ok32 && r8.PathsPerSec > 0 {
		fmt.Printf("8 workers ran at %s paths/sec, 32 at %s.\n", rate(r8.PathsPerSec), rate(r32.PathsPerSec))
		ratio := r32.PathsPerSec / r8.PathsPerSec
		switch {
		case ratio < 0.95:
			fmt.Printf("32 workers were %.2fx the 8-worker rate. This machine has %d CPUs, so the extras mostly wait.\n", ratio, runtime.NumCPU())
		case ratio > 1.15:
			fmt.Printf("32 workers still beat 8 (%.2fx). The machine had room past 8.\n", ratio)
		default:
			fmt.Printf("8 and 32 workers are close (%.2fx). Throughput flattens near the %d CPUs; cores in use went from %.1f to %.1f.\n", ratio, runtime.NumCPU(), r8.Cores, r32.Cores)
		}
		fmt.Printf("Against one core, 8 workers were %.2fx.\n", r8.PathsPerSec/r1.PathsPerSec)
	}
	if pool, ok := costs["pool/1"]; ok {
		if part, ok := costs["partition"]; ok && part.Elapsed > 0 {
			fmt.Printf("A channel send per path took %.2fx the static split. Batch 8192 narrows that gap.\n", float64(pool.Elapsed)/float64(part.Elapsed))
		}
	}
	if shared, ok := costs["shared-rng"]; ok {
		if part, ok := costs["partition"]; ok && part.Elapsed > 0 {
			fmt.Printf("One locked RNG took %.2fx private RNGs.\n", float64(shared.Elapsed)/float64(part.Elapsed))
		}
	}
	if locked, ok := costs["locked-agg"]; ok {
		if part, ok := costs["partition"]; ok && part.Elapsed > 0 {
			fmt.Printf("Locking the total on every path took %.2fx merging local totals at the end.\n", float64(locked.Elapsed)/float64(part.Elapsed))
		}
	}
	if naive, ok := costs["naive"]; ok && naive.PathsPerSec > 0 {
		fmt.Printf("Naive goroutines: %s paths/sec on %s paths, %s allocs. The scheduler, not the payoff, is the work.\n",
			rate(naive.PathsPerSec), comma(naive.Paths), comma64(naive.Mallocs))
	}
	if oneStep && big.Paths > 0 {
		fmt.Printf("100M paths finished in %s, %s allocs, %s retained. Keeping every payoff would be %s.\n",
			dur(big.Elapsed), comma64(big.Mallocs), bytes(big.Retained), bytes(big.Paths*8))
	} else {
		fmt.Println("Asian and barrier paths are streamed the same way. A 100M run is -paths 100000000 -mode partition.")
	}
}



func byWorkers(rows []Result, n int) (Result, bool) {
	for _, r := range rows {
		if r.Workers == n {
			return r, true
		}
	}
	return Result{}, false
}

func header() {
	fmt.Printf("%-12s %7s %12s %10s %9s %7s %10s %12s\n",
		"mode", "workers", "paths", "time", "paths/sec", "cores", "allocs", "mean")
}

func printRow(res Result) {
	label := res.Mode.String()
	if res.Mode == ModePool {
		label = fmt.Sprintf("pool/%d", res.Batch)
	}
	fmt.Printf("%-12s %7d %12s %10s %9s %7.2f %10s %12s\n",
		label, res.Workers, comma(res.Paths), dur(res.Elapsed), rate(res.PathsPerSec),
		res.Cores, comma64(res.Mallocs), num(res.Mean))
}

func reference(p Params) (float64, bool) {
	switch p.Product {
	case ProductGBM:
		return p.Spot * math.Exp(p.mu()*p.Years), true
	case ProductEuropean:
		if p.DriftSet && p.Drift != p.Rate {
			return 0, false
		}
		return blackScholesCall(p.Spot, p.Strike, p.Rate, p.Vol, p.Years), true
	default:
		return 0, false
	}
}



func buildAssets(spots, vols, qty []float64) ([]Asset, error) {
	if len(spots) == 0 {
		return defaultBook(), nil
	}
	if len(vols) != len(spots) {
		return nil, fmt.Errorf("spots and vols must be the same length")
	}
	if len(qty) != 0 && len(qty) != len(spots) {
		return nil, fmt.Errorf("qty must be empty or the same length as spots")
	}
	out := make([]Asset, len(spots))
	for i := range spots {
		q := 1.0
		if len(qty) != 0 {
			q = qty[i]
		}
		out[i] = Asset{Spot: spots[i], Vol: vols[i], Qty: q}
	}
	return out, nil
}

func parseFloats(s string) ([]float64, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float64, len(parts))
	for i, part := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q", part)
		}
		out[i] = v
	}
	return out, nil
}

var (
	cpuFile *os.File
)







