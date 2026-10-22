package analyticsstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/claudioed/returns-processing/internal/analytics/report"
)

// Memory is the in-memory twin of the analytical store: the same Projection
// and the same Reader semantics (earliest milestone wins, first descriptive
// value wins, dedupe on the CloudEvents id, dense day rows, zero-filled site
// entries, percentile_cont percentiles), so the same contract test can run
// against it and against Postgres. It is NOT the production store: nothing
// in cmd/ ever wires it.
type Memory struct {
	mu           sync.Mutex
	applied      map[string]time.Time // event id -> CloudEvents time
	lines        map[report.LineRef]*memoryLine
	dispositions map[string]*memoryDisposition
}

// memoryLine mirrors one return_line_facts row.
type memoryLine struct {
	siteID        string
	sku           string
	quantity      *int64
	rmaID         string
	dispositionID string
	opened        *time.Time
	inspected     *time.Time
	decided       *time.Time
	staged        *time.Time
}

// memoryDisposition mirrors one disposition_facts row.
type memoryDisposition struct {
	// ref is the disposition's receipt line: the site scope resolves
	// through it at query time (like the SQL LEFT JOIN), so a decision
	// whose line was not seen yet has no site.
	ref       report.LineRef
	version   int64
	outcome   string
	units     int64
	decidedAt time.Time
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{applied: map[string]time.Time{}, lines: map[report.LineRef]*memoryLine{}, dispositions: map[string]*memoryDisposition{}}
}

var _ report.Projection = (*Memory)(nil)
var _ report.Reader = (*Memory)(nil)

func least(existing, candidate *time.Time) *time.Time {
	if candidate == nil {
		return existing // LEAST ignores NULLs: a milestone this event does not mark keeps the stored value
	}
	if existing == nil || candidate.Before(*existing) {
		return candidate
	}
	return existing
}

func firstStr(existing, candidate string) string {
	if existing != "" {
		return existing // COALESCE: the first non-NULL value wins
	}
	return candidate
}

func firstQty(existing *int64, candidate int64) *int64 {
	if existing != nil {
		return existing
	}
	return &candidate
}

// Apply implements report.Projection with the same first-writer-wins
// semantics as the SQL store.
func (m *Memory) Apply(_ context.Context, e report.Event) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, seen := m.applied[e.EventID]; seen {
		return false, nil
	}
	m.applied[e.EventID] = e.At.UTC()

	if e.Kind == report.KindDispositionDecided {
		m.applyDisposition(e)
	}
	lines := report.LinesOf(e)
	cs := candidates(e)
	for i, c := range cs {
		m.applyLine(lines[i], c)
	}
	return true, nil
}

func (m *Memory) applyLine(ref report.LineRef, c lineCandidate) {
	l, ok := m.lines[ref]
	if !ok {
		l = &memoryLine{}
		m.lines[ref] = l
	}
	l.siteID = firstStr(l.siteID, c.siteID)
	l.sku = firstStr(l.sku, c.sku)
	l.rmaID = firstStr(l.rmaID, c.rmaID)
	l.dispositionID = firstStr(l.dispositionID, c.dispositionID)
	if c.quantity != 0 {
		l.quantity = firstQty(l.quantity, c.quantity)
	}
	l.opened = least(l.opened, c.m.Opened)
	l.inspected = least(l.inspected, c.m.Inspected)
	l.decided = least(l.decided, c.m.Decided)
	l.staged = least(l.staged, c.m.Staged)
}

func (m *Memory) applyDisposition(e report.Event) {
	m.dispositions[e.DispositionID] = &memoryDisposition{
		ref:     report.LineRef{ReceiptID: e.ReceiptID, LineNo: e.LineNo},
		version: e.PolicyVersion, outcome: e.Outcome,
		units: e.Quantity, decidedAt: e.At.UTC(),
	}
}

// in reports whether t falls in the half-open window [w.From, w.To); a nil
// time never does.
func in(t *time.Time, w report.DayWindow) bool {
	return t != nil && !t.Before(w.From) && t.Before(w.To)
}

func sum(q *int64) int64 {
	if q == nil {
		return 0
	}
	return *q
}

// siteOf answers the site a disposition's line sits at, for the site scope
// ("" when the line was not seen or has no site yet — the SQL LEFT JOIN's
// semantics).
func (m *Memory) siteOf(d *memoryDisposition) string {
	if l, ok := m.lines[d.ref]; ok {
		return l.siteID
	}
	return ""
}

// DispositionMix implements report.Reader: one row per (policy version,
// outcome) cell, in (version, outcome) order.
func (m *Memory) DispositionMix(_ context.Context, q report.MixQuery) ([]report.MixRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cells := map[[2]any]*report.MixRow{}
	for _, d := range m.dispositions {
		if d.decidedAt.Before(q.Range.From) || !d.decidedAt.Before(q.Range.To) {
			continue
		}
		if q.SiteID != "" && m.siteOf(d) != q.SiteID {
			continue
		}
		k := [2]any{d.version, d.outcome}
		cell, ok := cells[k]
		if !ok {
			cell = &report.MixRow{PolicyVersion: d.version, Outcome: d.outcome}
			cells[k] = cell
		}
		cell.Lines++
		cell.Units += d.units
	}
	out := make([]report.MixRow, 0, len(cells))
	for _, cell := range cells {
		out = append(out, *cell)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PolicyVersion != out[j].PolicyVersion {
			return out[i].PolicyVersion < out[j].PolicyVersion
		}
		return out[i].Outcome < out[j].Outcome
	})
	return out, nil
}

// sitesOf returns every site with a staged line in the range's scope, sorted.
func (m *Memory) sitesOf(q report.SiteQuery) []string {
	seen := map[string]bool{}
	for _, l := range m.lines {
		if l.siteID != "" && in(l.staged, report.DayWindow{From: q.Range.From, To: q.Range.To}) {
			if q.SiteID == "" || l.siteID == q.SiteID {
				seen[l.siteID] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ValueRecoveryDays implements report.Reader: one dense row per DayWindow,
// each with one zero-filled entry per site of the range's scope.
func (m *Memory) ValueRecoveryDays(_ context.Context, q report.SiteQuery) ([]report.RecoveryDay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sites := m.sitesOf(q)
	var out []report.RecoveryDay
	for _, w := range report.DayWindows(q.Range) {
		day := report.RecoveryDay{Day: w.Day, Sites: make([]report.RecoverySite, 0, len(sites))}
		for _, s := range sites {
			rs := report.RecoverySite{SiteID: s}
			for _, l := range m.lines {
				if l.siteID == s && in(l.staged, w) {
					rs.RestockedLines++
					rs.RestockedUnits += sum(l.quantity)
				}
			}
			day.Sites = append(day.Sites, rs)
		}
		out = append(out, day)
	}
	return out, nil
}

// CycleTimeDays implements report.Reader: one dense row per DayWindow.
func (m *Memory) CycleTimeDays(_ context.Context, q report.SiteQuery) ([]report.CycleDay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []report.CycleDay
	for _, w := range report.DayWindows(q.Range) {
		d := report.CycleDay{Day: w.Day}
		var latencies []float64
		for _, l := range m.lines {
			if q.SiteID != "" && l.siteID != q.SiteID {
				continue
			}
			if in(l.staged, w) {
				d.StagedLines++
				if l.opened != nil {
					latencies = append(latencies, max(0, l.staged.Sub(*l.opened).Seconds()))
				}
			}
		}
		d.CycleTime = report.TimingOf(latencies)
		out = append(out, d)
	}
	return out, nil
}

// LastEventAt implements report.Reader: the newest applied CloudEvents time.
func (m *Memory) LastEventAt(_ context.Context) (*time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var newest *time.Time
	for _, at := range m.applied {
		at := at
		if newest == nil || at.After(*newest) {
			newest = &at
		}
	}
	if newest != nil {
		utc := newest.UTC()
		return &utc, nil
	}
	return nil, nil
}
