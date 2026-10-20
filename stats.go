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
