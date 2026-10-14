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