	package main

import "testing"

func TestEstimateIsDeterministic(t *testing.T) {
	first, err := estimate(100_003, 4, 42)
	if err != nil {
		t.Fatalf("estimate returned an error: %v", err)
	}

	second, err := estimate(100_003, 4, 42)
	if err != nil {
		t.Fatalf("estimate returned an error: %v", err)
	}

	if first != second {
		t.Fatalf("estimate was not deterministic: got %d and %d hits", first, second)
	}
	if first < 0 || first > 100_003 {
		t.Fatalf("hit count out of range: %d", first)
	}
}

func TestEstimateCapsWorkersAtSamples(t *testing.T) {
	hits, err := estimate(1, 5, 42)
	if err != nil {
		t.Fatalf("estimate returned an error: %v", err)
	}
	if hits < 0 || hits > 1 {
		t.Fatalf("hit count out of range for one sample: %d", hits)
	}
}

func TestEstimateRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name    string
		samples int64
		workers int
	}{
		{name: "zero samples", samples: 0, workers: 1},
		{name: "negative samples", samples: -1, workers: 1},
		{name: "zero workers", samples: 1, workers: 0},
		{name: "negative workers", samples: 1, workers: -1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := estimate(test.samples, test.workers, 1); err == nil {
				t.Fatal("expected invalid arguments to return an error")
			}
		})
	}
}
