package report

import (
	"errors"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

func openedEv(id, receipt, site, when string, lines ...OpenedLine) Event {
	return Event{Kind: KindReturnReceiptOpened, EventID: id, At: at(when), SiteID: site, ReceiptID: receipt, Lines: lines}
}

func unexpectedEv(id, receipt, site, when string, line int32, sku string, qty int64) Event {
	return Event{Kind: KindUnexpectedReturnReceived, EventID: id, At: at(when), SiteID: site, ReceiptID: receipt, LineNo: line, SKU: sku, Quantity: qty}
}

func inspectedEv(id, receipt, when string, line int32, qty int64, grade string) Event {
	return Event{Kind: KindReturnLineInspected, EventID: id, At: at(when), ReceiptID: receipt, LineNo: line,
		SKU: "SKU-1", Quantity: qty, Grade: grade}
}

func decidedEv(id, disposition, receipt, when string, line int32, qty int64, grade, outcome string, version int64) Event {
	return Event{Kind: KindDispositionDecided, EventID: id, At: at(when), DispositionID: disposition, ReceiptID: receipt,
		LineNo: line, SKU: "SKU-1", Quantity: qty, Grade: grade, Outcome: outcome, PolicyVersion: version}
}

func stagedEv(id, disposition, receipt, site, when string, line int32, qty int64) Event {
	return Event{Kind: KindReturnLineStaged, EventID: id, At: at(when), DispositionID: disposition, ReceiptID: receipt,
		LineNo: line, SKU: "SKU-1", Quantity: qty, SiteID: site}
}

func TestKindsAreTheFiveProjectedEvents(t *testing.T) {
	got := Kinds()
	want := []Kind{KindReturnReceiptOpened, KindUnexpectedReturnReceived, KindReturnLineInspected, KindDispositionDecided, KindReturnLineStaged}
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kinds[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	if !KindReturnReceiptOpened.Known() || Kind("ReturnAuthorized").Known() {
		t.Fatal("Kind.Known must accept exactly the five projected kinds (the authorization events are not projected)")
	}
	if !KindReturnLineInspected.IsLine() || KindDispositionDecided.IsLine() {
		t.Fatal("IsLine must be true for the receipt-subject kinds only")
	}
	if !KindReturnLineStaged.IsDisposition() || KindReturnLineStaged.IsLine() {
		t.Fatal("IsDisposition must be true for the disposition-subject kinds only")
	}
}

func TestEventValidate(t *testing.T) {
	opened := openedEv("e1", "rcpt-1", "SITE-1", "2026-10-21T10:00:00Z",
		OpenedLine{LineNo: 1, SKU: "SKU-1", Quantity: 2, RMAID: "rma-1"},
		OpenedLine{LineNo: 2, SKU: "SKU-X", Quantity: 1})
	unexpected := unexpectedEv("e2", "rcpt-2", "SITE-1", "2026-10-21T10:00:00Z", 1, "SKU-X", 1)
	inspected := inspectedEv("e3", "rcpt-1", "2026-10-21T10:05:00Z", 1, 2, "A")
	decided := decidedEv("e4", "disp-1", "rcpt-1", "2026-10-21T10:06:00Z", 1, 2, "A", "RESTOCK", 3)
	staged := stagedEv("e5", "disp-1", "rcpt-1", "SITE-1", "2026-10-21T10:06:30Z", 1, 2)
	for _, e := range []Event{opened, unexpected, inspected, decided, staged} {
		if err := e.Validate(); err != nil {
			t.Fatalf("valid %s rejected: %v", e.Kind, err)
		}
	}
	cases := map[string]Event{}
	add := func(name string, e Event, mutate func(*Event)) {
		mutate(&e)
		cases[name] = e
	}
	add("unknown kind", opened, func(e *Event) { e.Kind = "ReturnAuthorized" })
	add("missing id", opened, func(e *Event) { e.EventID = "" })
	add("missing time", opened, func(e *Event) { e.At = time.Time{} })
	add("opened without receipt", opened, func(e *Event) { e.ReceiptID = "" })
	add("opened without lines", opened, func(e *Event) { e.Lines = nil })
	add("opened with a zero-quantity line", opened, func(e *Event) { e.Lines[0].Quantity = 0 })
	add("opened with a line without sku", opened, func(e *Event) { e.Lines[0].SKU = "" })
	add("unexpected without line no", unexpected, func(e *Event) { e.LineNo = 0 })
	add("unexpected without quantity", unexpected, func(e *Event) { e.Quantity = 0 })
	add("inspected with a bogus grade", inspected, func(e *Event) { e.Grade = "F" })
	add("inspected without quantity", inspected, func(e *Event) { e.Quantity = 0 })
	add("decided without receipt", decided, func(e *Event) { e.ReceiptID = "" })
	add("decided without disposition id", decided, func(e *Event) { e.DispositionID = "" })
	add("decided with a bogus outcome", decided, func(e *Event) { e.Outcome = "DONATE" })
	add("decided with a zero policy version", decided, func(e *Event) { e.PolicyVersion = 0 })
	add("staged without site", staged, func(e *Event) { e.SiteID = "" })
	add("staged without quantity", staged, func(e *Event) { e.Quantity = 0 })
	for name, e := range cases {
		if err := e.Validate(); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
}

func TestGradeAndOutcomeSpaces(t *testing.T) {
	for _, g := range []string{"A", "B", "C", GradeScrap} {
		if !ValidGrade(g) {
			t.Errorf("ValidGrade(%q) = false, want true", g)
		}
	}
	for _, g := range []string{"", "a", "F", "GOOD"} {
		if ValidGrade(g) {
			t.Errorf("ValidGrade(%q) = true, want false", g)
		}
	}
	for _, o := range []string{"RESTOCK", "REFURBISH", "LIQUIDATE", "DISPOSE", "RTV"} {
		if !ValidOutcome(o) {
			t.Errorf("ValidOutcome(%q) = false, want true", o)
		}
	}
	for _, o := range []string{"", "restock", "DONATE"} {
		if ValidOutcome(o) {
			t.Errorf("ValidOutcome(%q) = true, want false", o)
		}
	}
}

func TestMilestonesOf(t *testing.T) {
	g := at("2026-10-21T10:00:00Z")
	if m := MilestonesOf(openedEv("e", "r", "S", "2026-10-21T10:00:00Z", OpenedLine{LineNo: 1, SKU: "S", Quantity: 1})); !milestoneAt(m.Opened, g) || m.Inspected != nil || m.Decided != nil || m.Staged != nil {
		t.Fatalf("opened milestones = %+v", m)
	}
	if m := MilestonesOf(unexpectedEv("e", "r", "S", "2026-10-21T10:00:00Z", 1, "S", 1)); !milestoneAt(m.Opened, g) {
		t.Fatalf("unexpected milestones = %+v", m)
	}
	if m := MilestonesOf(inspectedEv("e", "r", "2026-10-21T10:00:00Z", 1, 1, "A")); !milestoneAt(m.Inspected, g) || m.Opened != nil {
		t.Fatalf("inspected milestones = %+v", m)
	}
	if m := MilestonesOf(decidedEv("e", "d", "r", "2026-10-21T10:00:00Z", 1, 1, "A", "RESTOCK", 1)); !milestoneAt(m.Decided, g) || m.Staged != nil {
		t.Fatalf("decided milestones = %+v", m)
	}
	if m := MilestonesOf(stagedEv("e", "d", "r", "S", "2026-10-21T10:00:00Z", 1, 1)); !milestoneAt(m.Staged, g) || m.Decided != nil {
		t.Fatalf("staged milestones = %+v", m)
	}
}

func milestoneAt(got *time.Time, want time.Time) bool {
	return got != nil && got.Equal(want)
}

func TestLinesOf(t *testing.T) {
	opened := openedEv("e", "rcpt-1", "S", "2026-10-21T10:00:00Z",
		OpenedLine{LineNo: 1, SKU: "A", Quantity: 1}, OpenedLine{LineNo: 2, SKU: "B", Quantity: 2})
	lines := LinesOf(opened)
	if len(lines) != 2 || lines[0] != (LineRef{ReceiptID: "rcpt-1", LineNo: 1}) || lines[1] != (LineRef{ReceiptID: "rcpt-1", LineNo: 2}) {
		t.Fatalf("opened lines = %+v", lines)
	}
	for _, e := range []Event{
		unexpectedEv("e", "rcpt-1", "S", "2026-10-21T10:00:00Z", 3, "A", 1),
		inspectedEv("e", "rcpt-1", "2026-10-21T10:00:00Z", 3, 1, "A"),
		decidedEv("e", "d", "rcpt-1", "2026-10-21T10:00:00Z", 3, 1, "A", "RESTOCK", 1),
		stagedEv("e", "d", "rcpt-1", "S", "2026-10-21T10:00:00Z", 3, 1),
	} {
		if lines := LinesOf(e); len(lines) != 1 || lines[0] != (LineRef{ReceiptID: e.ReceiptID, LineNo: e.LineNo}) {
			t.Fatalf("%s lines = %+v", e.Kind, lines)
		}
	}
}
