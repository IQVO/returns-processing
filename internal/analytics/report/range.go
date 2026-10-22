package report

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Granularity is the time-bucket resolution a report is rolled up to. The
// returns reports are operations-cadence reports (lines per site per day),
// so only daily buckets are modelled (the same decision as
// pick-face-replenishment's funnel).
type Granularity string

// GranularityDay rolls rows up into UTC day buckets (midnight UTC).
const GranularityDay Granularity = "day"

// ParseGranularity validates a raw `granularity` query value ("" = omitted,
// the day default). Every other value is rejected.
func ParseGranularity(raw string) (Granularity, error) {
	switch raw {
	case "":
		return GranularityDay, nil
	case string(GranularityDay):
		return GranularityDay, nil
	default:
		return "", fmt.Errorf("%w: granularity must be \"day\", got %q", ErrInvalidGranularity, raw)
	}
}

// ErrInvalidGranularity marks a granularity value this report cannot serve.
var ErrInvalidGranularity = errors.New("granularity must be \"day\"")

// Range is the half-open time range [From, To) the report answers for: a
// fact at exactly From is in, a fact at exactly To is out.
type Range struct {
	From time.Time
	To   time.Time
}

// DefaultWindow is the range used when from/to are omitted: the 30 days
// ending at "now". It is a function, not a constant, so mutation testing can
// reach (and the tests pin) the arithmetic.
func DefaultWindow() time.Duration { return 30 * 24 * time.Hour }

// MaxWindow is the longest range a caller may ask for: 366 days.
func MaxWindow() time.Duration { return 366 * 24 * time.Hour }

// Range errors; the HTTP adapter maps each to an RFC 7807 400.
var (
	ErrInvalidFrom   = errors.New("from must be an RFC 3339 timestamp")
	ErrInvalidTo     = errors.New("to must be an RFC 3339 timestamp")
	ErrEmptyRange    = errors.New("from must be strictly before to")
	ErrRangeTooLarge = errors.New("the range must not exceed 366 days")
)

// ParseRange validates the raw `from` / `to` query values (RFC 3339, "" =
// omitted) against now. Omitted bounds default so the range is the 30 days
// ending at now: no bounds -> [now-30d, now); only `to` -> [to-30d, to);
// only `from` -> [from, now). The result is in UTC and is rejected when
// empty/inverted (from >= to) or longer than MaxWindow.
func ParseRange(fromRaw, toRaw string, now time.Time) (Range, error) {
	to := now.UTC()
	if toRaw != "" {
		t, err := time.Parse(time.RFC3339, toRaw)
		if err != nil {
			return Range{}, fmt.Errorf("%w: %q", ErrInvalidTo, toRaw)
		}
		to = t.UTC()
	}
	from := to.Add(-DefaultWindow())
	if fromRaw != "" {
		t, err := time.Parse(time.RFC3339, fromRaw)
		if err != nil {
			return Range{}, fmt.Errorf("%w: %q", ErrInvalidFrom, fromRaw)
		}
		from = t.UTC()
	}
	if from.Compare(to) >= 0 {
		return Range{}, ErrEmptyRange
	}
	if to.Sub(from) > MaxWindow() {
		return Range{}, ErrRangeTooLarge
	}
	return Range{From: from, To: to}, nil
}

// DayWindow is the part of one UTC calendar day that lies inside a Range:
// [From, To), with Day the day's midnight UTC.
type DayWindow struct {
	Day  time.Time
	From time.Time
	To   time.Time
}

// DayWindows returns one window per UTC calendar day intersecting r, in
// order: the first starts at r.From, the last ends at r.To, every other one
// is a whole day. An empty or inverted range yields none.
func DayWindows(r Range) []DayWindow {
	from, to := r.From.UTC(), r.To.UTC()
	if from.Compare(to) >= 0 {
		return nil
	}
	y, m, d := from.Date()
	var out []DayWindow
	for day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC); day.Compare(to) < 0; day = day.AddDate(0, 0, 1) {
		out = append(out, DayWindow{Day: day, From: later(day, from), To: earlier(day.AddDate(0, 0, 1), to)})
	}
	return out
}

func later(a, b time.Time) time.Time {
	if a.Compare(b) >= 0 {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Compare(b) <= 0 {
		return a
	}
	return b
}

// Freshness is the projection's freshness (fleet analytics charter): AsOf is
// the CloudEvents time of the newest event applied and LagSeconds is
// now - AsOf. Both are nil while the projection has applied nothing.
type Freshness struct {
	AsOf       *time.Time `json:"as_of"`
	LagSeconds *float64   `json:"lag_seconds"`
}

// ComputeFreshness derives the lag from the newest applied event time. A
// clock that is behind the event (skew) reads as zero lag, never negative.
func ComputeFreshness(asOf *time.Time, now time.Time) Freshness {
	if asOf == nil {
		return Freshness{}
	}
	// math.Max, not `if lag < 0`: both yield 0 at the boundary, which would
	// leave an equivalent `<` -> `<=` mutant.
	lag := math.Max(0, now.Sub(*asOf).Seconds())
	utc := asOf.UTC()
	return Freshness{AsOf: &utc, LagSeconds: &lag}
}
