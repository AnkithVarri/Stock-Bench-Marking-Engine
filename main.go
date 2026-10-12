package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"sync"
	"time"
)

func main() {
	samples := flag.Int64("samples", 10_000_000, "number of random points to generate")
	workers := flag.Int("workers", runtime.GOMAXPROCS(0), "number of parallel workers")
	seed := flag.Int64("seed", 1, "random seed")
	flag.Parse()

	start := time.Now()
	hits, err := estimate(*samples, *workers, *seed)
	elapsed := time.Since(start)
	if err != nil {
		fmt.Fprintln(os.Stderr, "monte-carlo:", err)
		os.Exit(2)
	}

	
}

func estimate(samples int64, workers int, seed int64) (int64, error) {
	if samples <= 0 {
		return 0, fmt.Errorf("samples must be greater than zero")
	}
	if workers <= 0 {
		return 0, fmt.Errorf("workers must be greater than zero")
	}
	if int64(workers) > samples {
		workers = int(samples)
	}

	hits := make(chan int64, workers)
	var group sync.WaitGroup
	baseSamples := samples / int64(workers)
	remainder := samples % int64(workers)

	for worker := 0; worker < workers; worker++ {
		count := baseSamples
		if int64(worker) < remainder {
			count++
		}

		group.Add(1)
		go func(worker int, count int64) {
			defer group.Done()
			random := rand.New(rand.NewSource(seed + int64(worker)))
			var workerHits int64
			for i := int64(0); i < count; i++ {
				x, y := random.Float64(), random.Float64()
				if x*x+y*y <= 1 {
					workerHits++
				}
			}
			hits <- workerHits
		}(worker, count)
	}

	group.Wait()
	close(hits)

	var total int64
	for workerHits := range hits {
		total += workerHits
	}
	return total, nil
}
