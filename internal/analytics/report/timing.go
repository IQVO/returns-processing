package report

import (
	"math"
	"sort"
	"time"
)

// Timing summarises a set of durations: how many there were and their median
// and 95th percentile in seconds. P50 and P95 are nil while Count is 0.
type Timing struct {
	Count int
	P50   *float64
	P95   *float64
}

// Percentile is the p-quantile (0..1) of an ascending-sorted sample by linear
// interpolation between closest ranks: the same definition as PostgreSQL's
// percentile_cont, so the in-memory and SQL stores agree. It is 0 for an
// empty sample.
func Percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := p * float64(n-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo] + frac*(sorted[hi]-sorted[lo])
}

// TimingOf summarises durations in seconds (any order).
func TimingOf(seconds []float64) Timing {
	if len(seconds) == 0 {
		return Timing{}
	}
	s := append([]float64(nil), seconds...)
	sort.Float64s(s)
	p50, p95 := Percentile(s, 0.50), Percentile(s, 0.95)
	return Timing{Count: len(s), P50: &p50, P95: &p95}
}

// Seconds is d in seconds as a float.
func Seconds(d time.Duration) float64 { return d.Seconds() }
