package main

import (
	"fmt"
	"math"
)

type Product int

const (
	ProductGBM Product = iota
	ProductEuropean
	ProductAsian
	ProductBarrier
	ProductVaR
	ProductPortfolio
)

func (p Product) String() string {
	switch p {
	case ProductGBM:
		return "gbm"
	case ProductEuropean:
		return "european"
	case ProductAsian:
		return "asian"
	case ProductBarrier:
		return "barrier"
	case ProductVaR:
		return "var"
	case ProductPortfolio:
		return "portfolio"
	default:
		return "?"
	}
}

func ParseProduct(s string) (Product, error) {
	switch s {
	case "gbm":
		return ProductGBM, nil
	case "european":
		return ProductEuropean, nil
	case "asian":
		return ProductAsian, nil
	case "barrier":
		return ProductBarrier, nil
	case "var":
		return ProductVaR, nil
	case "portfolio":
		return ProductPortfolio, nil
	default:
		return 0, fmt.Errorf("product must be gbm, european, asian, barrier, var, or portfolio")
	}
}

func (p Product) oneStep() bool {
	return p == ProductGBM || p == ProductEuropean || p == ProductVaR || p == ProductPortfolio
}


func (p Product) needsHist() bool {
	return p == ProductVaR || p == ProductPortfolio
}

type Asset struct {
	Spot float64
	Vol  float64
	Qty  float64
}


type Params struct {
	Product  Product
	Mode     Mode
	Spot     float64
	Strike   float64
	Rate     float64
	Drift    float64
	DriftSet bool
	Vol      float64
	Years    float64
	Barrier  float64
	Steps    int
	Paths    int
	Workers  int
	Batch    int
	Seed     int64
	Force    bool
	Assets   []Asset
	Corr     []float64 // row-major, len n*n; empty means pairwise 0.4
}

func (p Params) mu() float64 {
	if p.DriftSet {
		return p.Drift
	}
	return p.Rate
}
