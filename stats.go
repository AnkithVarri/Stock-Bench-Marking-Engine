package main

import "math"

// moments is an online mean and variance. Paths are never kept.
type moments struct {
	n     int64
	sum   float64
	sumsq float64
}
