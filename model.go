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


func (p Params) validate() error {
	if p.Paths < 1 {
		return fmt.Errorf("paths must be >= 1")
	}
	if p.Workers < 0 {
		return fmt.Errorf("workers must be >= 0")
	}
	if p.Years < 0 || p.Vol < 0 || p.Spot <= 0 || p.Strike <= 0 {
		return fmt.Errorf("spot and strike must be > 0; vol and years must be >= 0")
	}
	if math.IsNaN(p.Rate) || math.IsInf(p.Rate, 0) {
		return fmt.Errorf("rate must be finite")
	}
	if p.Product == ProductBarrier && p.Barrier <= 0 {
		return fmt.Errorf("barrier must be > 0")
	}
	if p.Product == ProductAsian || p.Product == ProductBarrier {
		if p.Steps < 1 {
			return fmt.Errorf("steps must be >= 1")
		}
	}
	if p.Product == ProductPortfolio {
		n := len(p.Assets)
		if n < 2 {
			return fmt.Errorf("portfolio needs at least 2 assets")
		}
		if n > maxAssets {
			return fmt.Errorf("portfolio supports at most %d assets", maxAssets)
		}
		if len(p.Corr) != 0 && len(p.Corr) != n*n {
			return fmt.Errorf("corr must have %d entries, got %d", n*n, len(p.Corr))
		}
		for _, a := range p.Assets {
			if a.Spot <= 0 || a.Vol < 0 {
				return fmt.Errorf("each asset needs spot > 0 and vol >= 0")
			}
		}
	}
	if p.Mode == ModeNaive && p.Paths > naiveHardCap {
		return fmt.Errorf("naive mode caps at %d paths; one goroutine per path will not reach 100M", naiveHardCap)
	}
	if p.Mode == ModeNaive && p.Paths > naiveSoftCap && !p.Force {
		return fmt.Errorf("naive mode launches one goroutine per path; %d is above %d (pass -force to allow up to %d)", p.Paths, naiveSoftCap, naiveHardCap)
	}
	return nil
}



const maxAssets = 8

type leg struct {
	spot, qty float64
	drift     float64 // (mu - σ²/2) T
	diffuse   float64 // σ √T
}

type prepared struct {
	product   Product
	strike    float64
	barrier   float64
	disc      float64
	steps     int
	stepDrift float64
	stepVol   float64
	legs      []leg
	chol      [][]float64
	v0        float64
	mu        float64
	rate      float64
	histLo    float64
	histHi    float64
	closed    float64
	hasClosed bool
}

func prepare(p Params) (prepared, error) {
	pr := prepared{
		product: p.Product,
		strike:  p.Strike,
		barrier: p.Barrier,
		disc:    math.Exp(-p.Rate * p.Years),
		steps:   p.Steps,
		mu:      p.mu(),
		rate:    p.Rate,
	}
	assets := p.Assets
	if p.Product != ProductPortfolio {
		assets = []Asset{{Spot: p.Spot, Vol: p.Vol, Qty: 1}}
	}
	pr.legs = make([]leg, len(assets))
	for i, a := range assets {
		sig := a.Vol
		pr.legs[i] = leg{
			spot:    a.Spot,
			qty:     a.Qty,
			drift:   (pr.mu - 0.5*sig*sig) * p.Years,
			diffuse: sig * math.Sqrt(p.Years),
		}
		pr.v0 += a.Qty * a.Spot
	}
	if p.Product == ProductPortfolio {
		chol, err := cholesky(corrMatrix(p.Corr, len(assets)))
		if err != nil {
			return prepared{}, err
		}
		pr.chol = chol
	}
	if p.Steps > 0 && p.Years > 0 && p.Product != ProductPortfolio {
		dt := p.Years / float64(p.Steps)
		sig := p.Vol
		pr.stepDrift = (pr.mu - 0.5*sig*sig) * dt
		pr.stepVol = sig * math.Sqrt(dt)
	}
	pr.histLo, pr.histHi = lossRange(pr)
	pr.closed, pr.hasClosed = closedForm(p, pr)
	return pr, nil
}

func corrMatrix(flat []float64, n int) [][]float64 {
	a := make([][]float64, n)
	for i := range a {
		a[i] = make([]float64, n)
		for j := range a[i] {
			if len(flat) == 0 {
				if i == j {
					a[i][j] = 1
				} else {
					a[i][j] = 0.4
				}
				continue
			}
			a[i][j] = flat[i*n+j]
		}
	}
	return a
}

