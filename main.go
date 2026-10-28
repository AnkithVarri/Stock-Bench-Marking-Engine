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


