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
