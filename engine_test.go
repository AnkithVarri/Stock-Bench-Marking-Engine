package main

import (
	"math"
	"testing"
)

func euro() Params {
	return Params{
		Product: ProductEuropean,
		Mode:    ModePartition,
		Spot:    100, Strike: 100, Rate: 0.05, Vol: 0.2, Years: 1,
		Steps: 32, Paths: 80_000, Workers: 4, Seed: 1, Batch: 1024,
	}
}

func TestBlackScholes(t *testing.T) {
	got := blackScholesCall(100, 100, 0.05, 0.2, 1)
	const want = 10.450583572185565
	if math.Abs(got-want) > 1e-10 {
		t.Fatalf("bs = %.16f, want %.16f", got, want)
	}
}

func TestEuropeanNearAnalytic(t *testing.T) {
	p := euro()
	p.Paths = 200_000
	res, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	ref := blackScholesCall(p.Spot, p.Strike, p.Rate, p.Vol, p.Years)
	if res.StdErr <= 0 || math.Abs(res.Mean-ref) > 6*res.StdErr {
		t.Fatalf("mean %.6f stderr %.6f analytic %.6f", res.Mean, res.StdErr, ref)
	}
}

func TestSequentialMatchesOneWorker(t *testing.T) {
	p := euro()
	p.Paths = 20_000
	p.Workers = 1
	p.Mode = ModeSequential
	a, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Mode = ModePartition
	b, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Mean != b.Mean {
		t.Fatalf("sequential %.16f partition %.16f", a.Mean, b.Mean)
	}
}

func TestPartitionReproducible(t *testing.T) {
	p := euro()
	a, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Mean != b.Mean {
		t.Fatalf("two runs %.16f and %.16f", a.Mean, b.Mean)
	}
}

func TestAsianStepEqualsEuropean(t *testing.T) {
	// One fixing over the whole year is the European payoff.
	p := euro()
	p.Paths = 10_000
	p.Workers = 1
	p.Mode = ModeSequential
	p.Steps = 1
	eu, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Product = ProductAsian
	as, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(eu.Mean-as.Mean) > 1e-9 {
		t.Fatalf("european %.16f asian %.16f", eu.Mean, as.Mean)
	}
}

func TestZeroVolClosed(t *testing.T) {
	p := euro()
	p.Vol = 0
	p.Paths = 10
	res, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	want := math.Exp(-p.Rate*p.Years) * math.Max(0, p.Spot*math.Exp(p.Rate*p.Years)-p.Strike)
	if !res.Closed || math.Abs(res.Mean-want) > 1e-9 {
		t.Fatalf("closed=%v mean %.16f want %.16f", res.Closed, res.Mean, want)
	}
}

func TestBarrierAlreadyOut(t *testing.T) {
	p := euro()
	p.Product = ProductBarrier
	p.Barrier = 120
	p.Spot = 100
	res, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mean != 0 {
		t.Fatalf("knocked at t0, mean = %v", res.Mean)
	}
}

func TestBarrierZeroVolLives(t *testing.T) {
	p := euro()
	p.Product = ProductBarrier
	p.Vol = 0
	p.Rate = 0
	p.Barrier = 50
	p.Strike = 90
	res, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.Mean-10) > 1e-9 {
		t.Fatalf("mean %.16f, want 10", res.Mean)
	}
}

func TestGBMMean(t *testing.T) {
	p := euro()
	p.Product = ProductGBM
	p.Paths = 200_000
	res, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	want := p.Spot * math.Exp(p.Rate*p.Years)
	if math.Abs(res.Mean-want) > 6*res.StdErr {
		t.Fatalf("mean %.6f want %.6f stderr %.6f", res.Mean, want, res.StdErr)
	}
}

func TestVaRZeroVol(t *testing.T) {
	p := euro()
	p.Product = ProductVaR
	p.Vol = 0
	p.Rate = 0
	res, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mean != 0 || res.VaR95 != 0 || res.ES95 != 0 {
		t.Fatalf("mean %v var %v es %v", res.Mean, res.VaR95, res.ES95)
	}
}

func TestPortfolioRuns(t *testing.T) {
	p := euro()
	p.Product = ProductPortfolio
	p.Assets = defaultBook()
	p.Paths = 80_000
	res, err := Run(p)
	if err != nil {
		t.Fatal(err)
	}
	if res.StdErr <= 0 || math.IsNaN(res.VaR95) || math.IsNaN(res.ES99) {
		t.Fatalf("bad risk numbers %+v", res)
	}
	if res.ES99+1e-6 < res.VaR99 {
		t.Fatalf("ES %.6f below VaR %.6f", res.ES99, res.VaR99)
	}
}

func TestCholeskyRecoversCorrelation(t *testing.T) {
	flat := []float64{
		1, 0.8,
		0.8, 1,
	}
	l, err := cholesky(corrMatrix(flat, 2))
	if err != nil {
		t.Fatal(err)
	}
	// L L^T ≈ corr
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			dot := 0.0
			for k := 0; k < 2; k++ {
				dot += l[i][k] * l[j][k]
			}
			if math.Abs(dot-flat[i*2+j]) > 1e-9 {
				t.Fatalf("LLt[%d,%d]=%v", i, j, dot)
			}
		}
	}
}

func TestHistogramQuantile(t *testing.T) {
	h := makeHist(0, 20, 2)
	for i := 0; i < 95; i++ {
		h.add(1)
	}
	for i := 0; i < 5; i++ {
		h.add(15)
	}
	if math.Abs(h.quantile(0.95)-10) > 1e-9 {
		t.Fatalf("quantile = %v", h.quantile(0.95))
	}
	if math.Abs(h.es(0.95)-15) > 1e-6 {
		t.Fatalf("es = %v", h.es(0.95))
	}
}


func TestStreamingDoesNotGrowWithPaths(t *testing.T) {
	small := euro()
	small.Paths = 1_000
	small.Workers = 4
	a, err := Run(small)
	if err != nil {
		t.Fatal(err)
	}
	big := small
	big.Paths = 200_000
	b, err := Run(big)
	if err != nil {
		t.Fatal(err)
	}
	if a.Retained != b.Retained {
		t.Fatalf("retained grew from %d to %d", a.Retained, b.Retained)
	}
	if b.Mallocs > uint64(b.Paths)/100 {
		t.Fatalf("mallocs %d for %d paths", b.Mallocs, b.Paths)
	}
}

func TestNaiveRejectsHuge(t *testing.T) {
	p := euro()
	p.Mode = ModeNaive
	p.Paths = naiveSoftCap + 1
	if _, err := Run(p); err == nil {
		t.Fatal("expected the naive cap to trip")
	}
}

func TestModesNearAnalytic(t *testing.T) {
	p := euro()
	p.Paths = 60_000
	for _, mode := range []Mode{ModeSequential, ModeNaive, ModePool, ModePartition, ModeSharedRNG, ModeLockedAgg} {
		p.Mode = mode
		p.Workers = 4
		res, err := Run(p)
		if err != nil {
			t.Fatal(mode, err)
		}
		ref := blackScholesCall(100, 100, 0.05, 0.2, 1)
		if math.Abs(res.Mean-ref) > 8*res.StdErr {
			t.Fatalf("%s mean %.6f stderr %.6f analytic %.6f", mode, res.Mean, res.StdErr, ref)
		}
	}
}

var sink float64
