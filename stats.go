package main

import "math"

// moments is an online mean and variance. Paths are never kept.
type moments struct {
	n     int64
	sum   float64
	sumsq float64
}

func (m *moments) add(x float64) {
	m.n++
	m.sum += x
	m.sumsq += x * x
}

func (m *moments) merge(o moments) {
	m.n += o.n
	m.sum += o.sum
	m.sumsq += o.sumsq
}

func (m moments) meanStd() (mean, stderr float64) {
	if m.n == 0 {
		return 0, 0
	}
	mean = m.sum / float64(m.n)
	if m.n < 2 {
		return mean, 0
	}
	m2 := m.sumsq - m.sum*mean
	// Identical samples (zero vol) leave a rounding crumb.
	if m2 < 1e-12*math.Abs(m.sumsq) {
		m2 = 0
	}
	stderr = math.Sqrt((m2 / float64(m.n-1)) / float64(m.n))
	return mean, stderr
}


// hist is a fixed bin sketch of the loss distribution.

type hist struct {
	lo, width float64
	counts    []uint64
	sums      []float64
	total     uint64
	point     bool
}

func makeHist(lo, hi float64, bins int) *hist {
	h := &hist{}
	if bins < 1 {
		bins = 1
	}
	span := hi - lo
	if span < 1e-9 || math.IsNaN(span) {
		h.point = true
		h.counts = make([]uint64, 1)
		h.sums = make([]float64, 1)
		return h
	}
	pad := 0.01 * span
	h.lo = lo - pad
	h.width = (hi + pad - h.lo) / float64(bins)
	h.counts = make([]uint64, bins)
	h.sums = make([]float64, bins)
	return h
}

func (h *hist) clone() *hist {
	g := *h
	g.counts = make([]uint64, len(h.counts))
	g.sums = make([]float64, len(h.sums))
	g.total = 0
	return &g
}



func (h *hist) add(x float64) {
	h.total++
	if h.point {
		h.counts[0]++
		h.sums[0] += x
		return
	}
	i := int((x - h.lo) / h.width)
	if i < 0 {
		i = 0
	} else if i >= len(h.counts) {
		i = len(h.counts) - 1
	}
	h.counts[i]++
	h.sums[i] += x
}

func (h *hist) merge(o *hist) {
	if o == nil {
		return
	}
	h.total += o.total
	for i := range h.counts {
		h.counts[i] += o.counts[i]
		h.sums[i] += o.sums[i]
	}
}

func (h *hist) quantile(alpha float64) float64 {
	if h == nil || h.total == 0 {
		return 0
	}
	if h.point || h.width == 0 {
		return h.sums[0] / float64(h.counts[0])
	}
	target := alpha * float64(h.total)
	var cum uint64
	for i, c := range h.counts {
		next := cum + c
		if c > 0 && float64(next) >= target {
			frac := (target - float64(cum)) / float64(c)
			return h.lo + (float64(i)+frac)*h.width
		}
		cum = next
	}
	return h.lo + float64(len(h.counts))*h.width
}

// es is the mean of samples above the alpha quantile.
// Full bins use their real sums; the bin that contains the quantile is treated as uniform.
func (h *hist) es(alpha float64) float64 {
	if h == nil || h.total == 0 {
		return 0
	}
	q := h.quantile(alpha)
	if h.point || h.width == 0 {
		return q
	}
	var tailSum, tailN float64
	left := h.lo
	for i, c := range h.counts {
		right := left + h.width
		if c > 0 && right > q {
			if left >= q {
				tailSum += h.sums[i]
				tailN += float64(c)
			} else {
				frac := (right - q) / h.width
				tailN += frac * float64(c)
				tailSum += frac * float64(c) * (q + right) / 2
			}
		}
		left = right
	}
	if tailN == 0 {
		return q
	}
	return tailSum / tailN
}

func (h *hist) bytes() int {
	if h == nil {
		return 0
	}
	return len(h.counts)*8 + len(h.sums)*8
}

// acc is what one worker keeps. Merge at the end, instead of locking per path.
type acc struct {
	m moments
	h *hist
}

func (a *acc) observe(x float64) {
	a.m.add(x)
	if a.h != nil {
		a.h.add(x)
	}
}

func (a *acc) merge(b acc) {
	a.m.merge(b.m)
	if a.h != nil {
		a.h.merge(b.h)
	}
}

func (a acc) retained() int {
	return 32 + a.h.bytes()
}
