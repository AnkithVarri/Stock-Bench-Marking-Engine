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


func cholesky(a [][]float64) ([][]float64, error) {
	n := len(a)
	l := make([][]float64, n)
	for i := range l {
		l[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		for j := 0; j <= i; j++ {
			sum := a[i][j]
			for k := 0; k < j; k++ {
				sum -= l[i][k] * l[j][k]
			}
			if i == j {
				if sum <= 1e-12 {
					return nil, fmt.Errorf("correlation matrix is not positive definite")
				}
				l[i][j] = math.Sqrt(sum)
			} else {
				l[i][j] = sum / l[j][j]
			}
		}
	}
	return l, nil
}

func span(lg leg) (lo, hi float64) {
	if lg.diffuse == 0 {
		s := lg.spot * math.Exp(lg.drift)
		return s, s
	}
	return lg.spot * math.Exp(lg.drift-12*lg.diffuse), lg.spot * math.Exp(lg.drift+12*lg.diffuse)
}

func lossRange(pr prepared) (lo, hi float64) {
	switch pr.product {
	case ProductVaR, ProductPortfolio:
		vmin, vmax := 0.0, 0.0
		for _, lg := range pr.legs {
			sLo, sHi := span(lg)
			// qty can be short, so min and max flip.
			a, b := lg.qty*sLo, lg.qty*sHi
			if a > b {
				a, b = b, a
			}
			vmin += a
			vmax += b
		}
		return pr.v0 - vmax, pr.v0 - vmin
	default:
		return 0, 0
	}
}


func closedForm(p Params, pr prepared) (float64, bool) {
	if p.Years == 0 || (p.Product != ProductPortfolio && p.Vol == 0) || allZeroVol(pr) {
		switch p.Product {
		case ProductGBM:
			return pr.legs[0].spot * math.Exp(pr.mu*p.Years), true
		case ProductEuropean:
			st := pr.legs[0].spot * math.Exp(pr.mu*p.Years)
			return pr.disc * math.Max(0, st-pr.strike), true
		case ProductAsian:
			return asianClosed(pr.legs[0].spot, pr.mu, p.Years, pr.disc, pr.strike, p.Steps), true
		case ProductBarrier:
			st := pr.legs[0].spot * math.Exp(pr.mu*p.Years)
			if pr.legs[0].spot <= pr.barrier || st <= pr.barrier {
				return 0, true
			}
			return pr.disc * math.Max(0, st-pr.strike), true
		case ProductVaR, ProductPortfolio:
			loss := 0.0
			for _, lg := range pr.legs {
				st := lg.spot * math.Exp(pr.mu*p.Years)
				loss += lg.qty * (lg.spot - st)
			}
			return loss, true
		}
	}
	if p.Product == ProductBarrier && p.Spot <= p.Barrier {
		return 0, true
	}
	return 0, false
}



func allZeroVol(pr prepared) bool {
	for _, lg := range pr.legs {
		if lg.diffuse != 0 {
			return false
		}
	}
	return true
}

func asianClosed(spot, mu, years, disc, strike float64, steps int) float64 {
	if years == 0 || steps < 1 || mu == 0 {
		return disc * math.Max(0, spot-strike)
	}
	dt := years / float64(steps)
	r := math.Exp(mu * dt)
	sum := spot * r * (1 - math.Pow(r, float64(steps))) / (1 - r)
	return disc * math.Max(0, sum/float64(steps)-strike)
}

func terminal(lg leg, z float64) float64 {
	return lg.spot * math.Exp(lg.drift+lg.diffuse*z)
}

func consume[R interface{ NormFloat64() float64 }](pr *prepared, rng R, n int, dst *acc) {
	switch pr.product {
	case ProductAsian, ProductBarrier:
		for i := 0; i < n; i++ {
			dst.observe(pathPayoff(pr, rng))
		}
	case ProductPortfolio:
		for i := 0; i < n; i++ {
			dst.observe(bookLoss(pr, rng))
		}
	default:
		oneStep(pr, rng, n, dst)
	}
}

func oneStep[R interface{ NormFloat64() float64 }](pr *prepared, rng R, n int, dst *acc) {
	lg := pr.legs[0]
	for i := 0; i < n; i++ {
		st := terminal(lg, rng.NormFloat64())
		switch pr.product {
		case ProductGBM:
			dst.observe(st)
		case ProductEuropean:
			c := st - pr.strike
			if c < 0 {
				c = 0
			}
			dst.observe(pr.disc * c)
		default: // single-asset loss
			dst.observe(lg.qty * (lg.spot - st))
		}
	}
}


func pathPayoff[R interface{ NormFloat64() float64 }](pr *prepared, rng R) float64 {
	lg := pr.legs[0]
	if pr.product == ProductBarrier && lg.spot <= pr.barrier {
		return 0
	}
	logS := math.Log(lg.spot)
	sum := 0.0
	last := lg.spot
	for i := 0; i < pr.steps; i++ {
		logS += pr.stepDrift + pr.stepVol*rng.NormFloat64()
		s := math.Exp(logS)
		last = s
		sum += s
		if pr.product == ProductBarrier && s <= pr.barrier {
			return 0
		}
	}
	level := last
	if pr.product == ProductAsian {
		level = sum / float64(pr.steps) // fixings after t0, not including spot
	}
	c := level - pr.strike
	if c < 0 {
		c = 0
	}
	return pr.disc * c
}

func bookLoss[R interface{ NormFloat64() float64 }](pr *prepared, rng R) float64 {
	var z [maxAssets]float64
	n := len(pr.legs)
	for i := 0; i < n; i++ {
		z[i] = rng.NormFloat64()
	}
	v := 0.0
	for i := 0; i < n; i++ {
		cz := 0.0
		row := pr.chol[i]
		for k := 0; k <= i; k++ {
			cz += row[k] * z[k]
		}
		v += pr.legs[i].qty * terminal(pr.legs[i], cz)
	}
	return pr.v0 - v
}



func blackScholesCall(spot, strike, rate, vol, years float64) float64 {
	if years == 0 || vol == 0 {
		st := spot * math.Exp(rate*years)
		return math.Exp(-rate*years) * math.Max(0, st-strike)
	}
	sqrtT := math.Sqrt(years)
	d1 := (math.Log(spot/strike) + (rate+0.5*vol*vol)*years) / (vol * sqrtT)
	d2 := d1 - vol*sqrtT
	return spot*normCDF(d1) - strike*math.Exp(-rate*years)*normCDF(d2)
}

func normCDF(x float64) float64 {
	return 0.5 * math.Erfc(-x/math.Sqrt2)
}

func defaultBook() []Asset {
	return []Asset{
		{Spot: 100, Vol: 0.20, Qty: 1},
		{Spot: 80, Vol: 0.25, Qty: 1},
		{Spot: 50, Vol: 0.30, Qty: 1},
	}
}


