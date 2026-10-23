package analyticsstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/analytics/report"
)

// store is what the contract exercises: the writer and the reader over one
// model. The SAME contract runs against Memory (unit) and Postgres
// (integration), so the two stores cannot drift.
type store interface {
	report.Projection
	report.Reader
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func opened(id, receipt, site, when string, lines ...report.OpenedLine) report.Event {
	return report.Event{Kind: report.KindReturnReceiptOpened, EventID: id, At: at(when), SiteID: site, ReceiptID: receipt, Lines: lines}
}

func unexpected(id, receipt, site, when string, line int32, sku string, qty int64) report.Event {
	return report.Event{Kind: report.KindUnexpectedReturnReceived, EventID: id, At: at(when), SiteID: site, ReceiptID: receipt, LineNo: line, SKU: sku, Quantity: qty}
}

func inspected(id, receipt, when string, line int32, qty int64, grade string) report.Event {
	return report.Event{Kind: report.KindReturnLineInspected, EventID: id, At: at(when), ReceiptID: receipt, LineNo: line,
		SKU: "SKU-1", Quantity: qty, Grade: grade}
}

func decided(id, disposition, receipt, when string, line int32, qty int64, grade, outcome string, version int64) report.Event {
	return report.Event{Kind: report.KindDispositionDecided, EventID: id, At: at(when), DispositionID: disposition, ReceiptID: receipt,
		LineNo: line, SKU: "SKU-1", Quantity: qty, Grade: grade, Outcome: outcome, PolicyVersion: version}
}

func staged(id, disposition, receipt, site, when string, line int32, qty int64) report.Event {
	return report.Event{Kind: report.KindReturnLineStaged, EventID: id, At: at(when), DispositionID: disposition, ReceiptID: receipt,
		LineNo: line, SKU: "SKU-1", Quantity: qty, SiteID: site}
}

func mustApply(t *testing.T, s store, events ...report.Event) {
	t.Helper()
	for _, e := range events {
		if _, err := s.Apply(context.Background(), e); err != nil {
			t.Fatalf("apply %s %s: %v", e.Kind, e.EventID, err)
		}
	}
}

func mix(t *testing.T, s store, from, to, site string) []report.MixRow {
	t.Helper()
	got, err := s.DispositionMix(context.Background(), report.MixQuery{Range: report.Range{From: at(from), To: at(to)}, SiteID: site})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func recovery(t *testing.T, s store, from, to, site string) []report.RecoveryDay {
	t.Helper()
	got, err := s.ValueRecoveryDays(context.Background(), report.SiteQuery{
		Range: report.Range{From: at(from), To: at(to)}, SiteID: site, Granularity: report.GranularityDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func cycle(t *testing.T, s store, from, to, site string) []report.CycleDay {
	t.Helper()
	got, err := s.CycleTimeDays(context.Background(), report.SiteQuery{
		Range: report.Range{From: at(from), To: at(to)}, SiteID: site, Granularity: report.GranularityDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func approx(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(*a-*b) < 1e-6
}

func f(v float64) *float64 { return &v }

// runContract is the shared behavioural contract of the analytical store.
func runContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	runWriterContract(t, newStore)
	runMilestonesContract(t, newStore)
	runMixContract(t, newStore)
	runRecoveryContract(t, newStore)
	runCycleContract(t, newStore)
	runFreshnessContract(t, newStore)
}

// runWriterContract pins the writer semantics: deterministic rejection of
// invalid events and idempotent replay.
func runWriterContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("invalid events are rejected deterministically", func(t *testing.T) {
		s := newStore(t)
		bad := decided("e1", "", "rcpt-1", "2026-10-21T10:06:00Z", 1, 2, "A", "RESTOCK", 1)
		if _, err := s.Apply(context.Background(), bad); !errors.Is(err, report.ErrRejected) {
			t.Fatalf("err = %v, want ErrRejected", err)
		}
	})

	t.Run("a replayed event id is a no-op", func(t *testing.T) {
		s := newStore(t)
		e := staged("e1", "disp-1", "rcpt-1", "SITE-1", "2026-10-21T10:06:30Z", 1, 2)
		if applied, err := s.Apply(context.Background(), e); err != nil || !applied {
			t.Fatalf("first apply = %v, %v", applied, err)
		}
		if applied, err := s.Apply(context.Background(), e); err != nil || applied {
			t.Fatalf("replay = %v, %v; want false, nil", applied, err)
		}
		days := recovery(t, s, "2026-10-21T00:00:00Z", "2026-10-22T00:00:00Z", "SITE-1")
		if len(days) != 1 || len(days[0].Sites) != 1 || days[0].Sites[0].RestockedLines != 1 || days[0].Sites[0].RestockedUnits != 2 {
			t.Fatalf("days = %+v, want one staged line counted once", days)
		}
	})
}

// runMilestonesContract pins that milestones converge whatever order the
// events arrive in, and that an opened receipt's lines all appear.
func runMilestonesContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("milestones converge whatever order the events arrive in", func(t *testing.T) {
		s := newStore(t)
		// Staged before opened, decided before inspected: the fact rows
		// still converge on the earliest time per milestone.
		mustApply(t, s,
			staged("e5", "disp-1", "rcpt-1", "SITE-1", "2026-10-21T10:06:30Z", 1, 2),
			decided("e4", "disp-1", "rcpt-1", "2026-10-21T10:06:00Z", 1, 2, "A", "RESTOCK", 1),
			inspected("e3", "rcpt-1", "2026-10-21T10:05:00Z", 1, 2, "A"),
			opened("e1", "rcpt-1", "SITE-1", "2026-10-21T10:00:00Z", report.OpenedLine{LineNo: 1, SKU: "SKU-1", Quantity: 2, RMAID: "rma-1"}),
		)
		days := cycle(t, s, "2026-10-21T00:00:00Z", "2026-10-22T00:00:00Z", "")
		if len(days) != 1 || days[0].StagedLines != 1 {
			t.Fatalf("days = %+v, want one staged line", days)
		}
		// opened 10:00, staged 10:06:30 = 390 seconds.
		wantLatency(t, days[0].CycleTime, 1, f(390), f(390))
	})

	t.Run("an opened receipt marks every carried line", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			opened("e1", "rcpt-1", "SITE-1", "2026-10-21T10:00:00Z",
				report.OpenedLine{LineNo: 1, SKU: "SKU-1", Quantity: 2, RMAID: "rma-1"},
				report.OpenedLine{LineNo: 2, SKU: "SKU-X", Quantity: 1}),
			unexpected("e2", "rcpt-1", "SITE-1", "2026-10-21T10:00:00Z", 3, "SKU-Y", 1),
		)
		days := cycle(t, s, "2026-10-21T00:00:00Z", "2026-10-22T00:00:00Z", "")
		if len(days) != 1 || days[0].StagedLines != 0 {
			t.Fatalf("days = %+v, want no staged line yet", days)
		}
		// Staging line 3 only: lines 1 and 2 stay open (no staged_at).
		mustApply(t, s, staged("e3", "disp-9", "rcpt-1", "SITE-1", "2026-10-21T11:00:00Z", 3, 1))
		days = cycle(t, s, "2026-10-21T00:00:00Z", "2026-10-22T00:00:00Z", "")
		if days[0].StagedLines != 1 {
			t.Fatalf("days = %+v, want exactly the staged line 3", days)
		}
	})
}

// runMixContract pins the disposition mix: cells by policy version and
// outcome, over the decision window, with the optional site scope.
func runMixContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("cells aggregate by policy version and outcome", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			opened("o1", "rcpt-1", "SITE-1", "2026-10-21T10:00:00Z",
				report.OpenedLine{LineNo: 1, SKU: "SKU-1", Quantity: 2},
				report.OpenedLine{LineNo: 2, SKU: "SKU-1", Quantity: 3}),
			opened("o2", "rcpt-2", "SITE-2", "2026-10-21T10:00:00Z",
				report.OpenedLine{LineNo: 1, SKU: "SKU-2", Quantity: 4}),
			decided("d1", "disp-1", "rcpt-1", "2026-10-21T10:06:00Z", 1, 2, "A", "RESTOCK", 1),
			decided("d2", "disp-2", "rcpt-1", "2026-10-21T10:07:00Z", 2, 3, "C", "LIQUIDATE", 1),
			decided("d3", "disp-3", "rcpt-2", "2026-10-22T10:06:00Z", 1, 4, "A", "RESTOCK", 2),
		)
		rows := mix(t, s, "2026-10-21T00:00:00Z", "2026-10-23T00:00:00Z", "")
		want := []report.MixRow{
			{PolicyVersion: 1, Outcome: "LIQUIDATE", Lines: 1, Units: 3},
			{PolicyVersion: 1, Outcome: "RESTOCK", Lines: 1, Units: 2},
			{PolicyVersion: 2, Outcome: "RESTOCK", Lines: 1, Units: 4},
		}
		if len(rows) != len(want) {
			t.Fatalf("rows = %+v, want %+v", rows, want)
		}
		for i := range want {
			if rows[i] != want[i] {
				t.Fatalf("row %d = %+v, want %+v", i, rows[i], want[i])
			}
		}
		// The range clips to the decision time.
		rows = mix(t, s, "2026-10-21T00:00:00Z", "2026-10-22T00:00:00Z", "")
		if len(rows) != 2 {
			t.Fatalf("clipped rows = %+v, want the two day-1 decisions", rows)
		}
		// The site scope narrows through the receipt line.
		rows = mix(t, s, "2026-10-21T00:00:00Z", "2026-10-23T00:00:00Z", "SITE-2")
		if len(rows) != 1 || rows[0].PolicyVersion != 2 || rows[0].Lines != 1 || rows[0].Units != 4 {
			t.Fatalf("SITE-2 rows = %+v", rows)
		}
	})
}

// runRecoveryContract pins the value-recovery report: dense days,
// zero-filled sites, staged lines and units per site.
func runRecoveryContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("days are dense and sites are zero-filled", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			opened("o1", "rcpt-1", "SITE-1", "2026-10-21T10:00:00Z", report.OpenedLine{LineNo: 1, SKU: "SKU-1", Quantity: 2}),
			staged("s1", "disp-1", "rcpt-1", "SITE-1", "2026-10-21T10:06:30Z", 1, 2),
			opened("o2", "rcpt-2", "SITE-2", "2026-10-21T10:00:00Z", report.OpenedLine{LineNo: 1, SKU: "SKU-2", Quantity: 5}),
			staged("s2", "disp-2", "rcpt-2", "SITE-2", "2026-10-22T09:00:00Z", 1, 5),
		)
		days := recovery(t, s, "2026-10-21T00:00:00Z", "2026-10-24T00:00:00Z", "")
		if len(days) != 3 {
			t.Fatalf("days = %d, want 3 dense days", len(days))
		}
		if len(days[0].Sites) != 2 || days[0].Sites[0].SiteID != "SITE-1" || days[0].Sites[1].SiteID != "SITE-2" {
			t.Fatalf("day 1 sites = %+v", days[0].Sites)
		}
		if days[0].Sites[0] != (report.RecoverySite{SiteID: "SITE-1", RestockedLines: 1, RestockedUnits: 2}) ||
			days[0].Sites[1] != (report.RecoverySite{SiteID: "SITE-2"}) {
			t.Fatalf("day 1 = %+v", days[0].Sites)
		}
		if days[1].Sites[0].RestockedLines != 0 || days[1].Sites[1] != (report.RecoverySite{SiteID: "SITE-2", RestockedLines: 1, RestockedUnits: 5}) {
			t.Fatalf("day 2 = %+v", days[1].Sites)
		}
		if days[2].Sites[0].RestockedLines != 0 || days[2].Sites[1].RestockedLines != 0 {
			t.Fatalf("day 3 = %+v, want zeros", days[2].Sites)
		}
	})

	t.Run("the site scope narrows to one site", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			opened("o1", "rcpt-1", "SITE-1", "2026-10-21T10:00:00Z", report.OpenedLine{LineNo: 1, SKU: "SKU-1", Quantity: 2}),
			staged("s1", "disp-1", "rcpt-1", "SITE-1", "2026-10-21T10:06:30Z", 1, 2),
			opened("o2", "rcpt-2", "SITE-2", "2026-10-21T10:00:00Z", report.OpenedLine{LineNo: 1, SKU: "SKU-2", Quantity: 5}),
			staged("s2", "disp-2", "rcpt-2", "SITE-2", "2026-10-22T09:00:00Z", 1, 5),
		)
		days := recovery(t, s, "2026-10-21T00:00:00Z", "2026-10-24T00:00:00Z", "SITE-2")
		if len(days) != 3 || len(days[0].Sites) != 1 || days[0].Sites[0].SiteID != "SITE-2" {
			t.Fatalf("scoped days = %+v", days)
		}
	})
}

// runCycleContract pins the return-cycle-time report: staged counts and the
// opened-to-staged percentiles.
func runCycleContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("cycle percentiles are linear-interpolated and need both milestones", func(t *testing.T) {
		s := newStore(t)
		// Four lines opened at 08:00 and staged 1h..4h later; a fifth staged
		// without a seen opening counts but has no latency.
		for i := 1; i <= 4; i++ {
			mustApply(t, s,
				opened(fmt.Sprintf("o%d", i), fmt.Sprintf("rcpt-%d", i), "SITE-1", "2026-10-21T08:00:00Z",
					report.OpenedLine{LineNo: 1, SKU: "SKU-1", Quantity: 1}),
				staged(fmt.Sprintf("s%d", i), fmt.Sprintf("disp-%d", i), fmt.Sprintf("rcpt-%d", i), "SITE-1",
					fmt.Sprintf("2026-10-21T%02d:00:00Z", 9+i-1), 1, 1),
			)
		}
		mustApply(t, s, staged("s9", "disp-9", "rcpt-9", "SITE-1", "2026-10-21T12:00:00Z", 1, 1))
		days := cycle(t, s, "2026-10-21T00:00:00Z", "2026-10-22T00:00:00Z", "")
		if len(days) != 1 || days[0].StagedLines != 5 {
			t.Fatalf("days = %+v, want five staged lines", days)
		}
		// Latencies 1h, 2h, 3h, 4h: p50 = 2h30, p95 = 3h51.
		wantLatency(t, days[0].CycleTime, 4, f(9000), f(13860))
		// The site scope narrows the counts.
		days = cycle(t, s, "2026-10-21T00:00:00Z", "2026-10-22T00:00:00Z", "SITE-2")
		if len(days) != 1 || days[0].StagedLines != 0 || days[0].CycleTime.Count != 0 {
			t.Fatalf("scoped days = %+v, want zeros", days)
		}
	})

	t.Run("a negative duration reads as zero, never negative", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			staged("s1", "disp-1", "rcpt-1", "SITE-1", "2026-10-21T10:00:00Z", 1, 1),
			opened("o1", "rcpt-1", "SITE-1", "2026-10-21T11:00:00Z", report.OpenedLine{LineNo: 1, SKU: "SKU-1", Quantity: 1}),
		)
		days := cycle(t, s, "2026-10-21T00:00:00Z", "2026-10-22T00:00:00Z", "")
		wantLatency(t, days[0].CycleTime, 1, f(0), f(0))
	})
}

// runFreshnessContract pins the freshness basis: the newest applied event.
func runFreshnessContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("freshness is the newest applied event time", func(t *testing.T) {
		s := newStore(t)
		if got, err := s.LastEventAt(context.Background()); err != nil || got != nil {
			t.Fatalf("empty LastEventAt = %v, %v; want nil", got, err)
		}
		mustApply(t, s,
			opened("e1", "rcpt-1", "SITE-1", "2026-10-21T10:00:00Z", report.OpenedLine{LineNo: 1, SKU: "SKU-1", Quantity: 1}),
			staged("e2", "disp-1", "rcpt-2", "SITE-1", "2026-10-23T09:00:00Z", 1, 1),
			inspected("e3", "rcpt-1", "2026-10-22T09:00:00Z", 1, 1, "A"),
		)
		got, err := s.LastEventAt(context.Background())
		if err != nil || got == nil || !got.Equal(at("2026-10-23T09:00:00Z")) {
			t.Fatalf("LastEventAt = %v, %v; want 2026-10-23T09:00:00Z", got, err)
		}
	})
}

func wantLatency(t *testing.T, got report.Timing, count int, p50, p95 *float64) {
	t.Helper()
	if got.Count != count || !approx(got.P50, p50) || !approx(got.P95, p95) {
		t.Fatalf("timing = %+v, want count %d p50 %v p95 %v", got, count, p50, p95)
	}
}

func TestMemoryContract(t *testing.T) {
	runContract(t, func(*testing.T) store { return NewMemory() })
}
