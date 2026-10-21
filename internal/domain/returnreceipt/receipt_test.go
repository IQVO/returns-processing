package returnreceipt

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

var opened = time.Date(2026, 10, 22, 14, 5, 0, 0, time.UTC)

func matchedLine(t *testing.T) Line {
	t.Helper()
	l, err := MatchedLine(1, "SKU-1", 2, "rma-1")
	if err != nil {
		t.Fatalf("MatchedLine: %v", err)
	}
	return l
}

func unexpectedLine(t *testing.T) Line {
	t.Helper()
	l, err := UnexpectedLine(2, "SKU-9", 1)
	if err != nil {
		t.Fatalf("UnexpectedLine: %v", err)
	}
	return l
}

// The invariant: every line references an RMA line or is UNEXPECTED — the
// constructor refuses a third state. A zero Line (no kind), a matched line
// without its rma and an unexpected line with one are all refused.
func TestConstructorRefusesThirdLineState(t *testing.T) {
	unexpectedWithRMA, err := UnexpectedLine(1, "SKU-1", 1)
	if err != nil {
		t.Fatalf("UnexpectedLine: %v", err)
	}
	unexpectedWithRMA.rmaID = shared.MustID("rma-1") // the state no constructor can build
	cases := []struct {
		name string
		line Line
		want error
	}{
		{"zero line (no kind)", Line{}, ErrLineKindMissing},
		{"matched without rma", Line{kind: KindMatched, rmaID: shared.ID{}}, ErrMatchedLineWithoutRMA},
		{"unexpected with rma", unexpectedWithRMA, ErrUnexpectedLineWithRMA},
	}
	for _, tc := range cases {
		_, err := Open(OpenInput{ReceiptID: "rcpt-1", SiteID: "WH1", At: opened, Lines: []Line{tc.line}})
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: Open = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := MatchedLine(0, "SKU-1", 1, "rma-1"); !errors.Is(err, shared.ErrLineNoBelowOne) {
		t.Fatalf("MatchedLine line 0 = %v", err)
	}
	if _, err := MatchedLine(1, "", 1, "rma-1"); !errors.Is(err, shared.ErrBlankID) {
		t.Fatalf("MatchedLine blank sku = %v", err)
	}
	if _, err := MatchedLine(1, "SKU-1", 0, "rma-1"); !errors.Is(err, shared.ErrQuantityBelowOne) {
		t.Fatalf("MatchedLine qty 0 = %v", err)
	}
	if _, err := MatchedLine(1, "SKU-1", 1, ""); !errors.Is(err, shared.ErrBlankID) {
		t.Fatalf("MatchedLine blank rma = %v", err)
	}
	if _, err := UnexpectedLine(0, "SKU-1", 1); !errors.Is(err, shared.ErrLineNoBelowOne) {
		t.Fatalf("UnexpectedLine line 0 = %v", err)
	}
	if !KindMatched.Valid() || LineKind("x").Valid() {
		t.Fatal("kind validity")
	}
}

// The happy path: opening a receipt with one matched and one unexpected line
// raises ReturnReceiptOpened with both lines, the unexpected one carrying no rma.
func TestOpenRaisesReceiptOpenedEvent(t *testing.T) {
	r, err := Open(OpenInput{
		ReceiptID: "rcpt-1", RMAID: "rma-1", SiteID: "WH1", At: opened,
		Lines: []Line{matchedLine(t), unexpectedLine(t)},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r.Version() != 1 || r.RMAID() != "rma-1" || r.SiteID() != "WH1" || r.OpenedAt() != opened {
		t.Fatalf("receipt = %v %v %v", r.Version(), r.RMAID(), r.SiteID())
	}
	evs := r.DomainEvents()
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (opened + one unexpected)", len(evs))
	}
	openedEv, ok := evs[0].(ReturnReceiptOpened)
	if !ok || openedEv.EventName() != "ReturnReceiptOpened" {
		t.Fatalf("first event = %T", evs[0])
	}
	if len(openedEv.Lines) != 2 || openedEv.Lines[0].RMAID != "rma-1" || openedEv.Lines[1].RMAID != "" {
		t.Fatalf("opened lines = %+v", openedEv.Lines)
	}
}

// The happy path's second event: one UnexpectedReturnReceived per unexpected
// line, carrying the site and the line facts.
func TestOpenRaisesUnexpectedReturnReceived(t *testing.T) {
	r, err := Open(OpenInput{
		ReceiptID: "rcpt-1", RMAID: "rma-1", SiteID: "WH1", At: opened,
		Lines: []Line{matchedLine(t), unexpectedLine(t)},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	evs := r.DomainEvents()
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (opened + one unexpected)", len(evs))
	}
	unexp, ok := evs[1].(UnexpectedReturnReceived)
	if !ok || unexp.LineNo != 2 || unexp.SKU != "SKU-9" {
		t.Fatalf("unexpected event = %+v", unexp)
	}
	if unexp.Quantity != 1 || unexp.SiteID != "WH1" {
		t.Fatalf("unexpected event = %+v", unexp)
	}
}

// The failing paths of Open: blank receipt id, blank site, zero time, no
// lines, duplicate line numbers, a bad receipt-level rma.
func TestOpenValidation(t *testing.T) {
	good := func(t *testing.T) OpenInput {
		return OpenInput{ReceiptID: "rcpt-1", RMAID: "rma-1", SiteID: "WH1", At: opened,
			Lines: []Line{matchedLine(t)}}
	}
	t.Run("blank receipt id", func(t *testing.T) {
		in := good(t)
		in.ReceiptID = " "
		if _, err := Open(in); !errors.Is(err, shared.ErrBlankID) {
			t.Fatalf("Open = %v", err)
		}
	})
	t.Run("blank site", func(t *testing.T) {
		in := good(t)
		in.SiteID = ""
		if _, err := Open(in); !errors.Is(err, shared.ErrBlankID) {
			t.Fatalf("Open = %v", err)
		}
	})
	t.Run("zero time", func(t *testing.T) {
		in := good(t)
		in.At = time.Time{}
		if _, err := Open(in); !errors.Is(err, ErrNoTimestamp) {
			t.Fatalf("Open = %v", err)
		}
	})
	t.Run("no lines", func(t *testing.T) {
		in := good(t)
		in.Lines = nil
		if _, err := Open(in); !errors.Is(err, ErrNoLines) {
			t.Fatalf("Open = %v", err)
		}
	})
	t.Run("duplicate line numbers", func(t *testing.T) {
		in := good(t)
		in.Lines = []Line{matchedLine(t), matchedLine(t)}
		if _, err := Open(in); !errors.Is(err, ErrDuplicateLineNo) {
			t.Fatalf("Open = %v", err)
		}
	})
	t.Run("bad receipt rma", func(t *testing.T) {
		in := good(t)
		in.RMAID = " "
		if _, err := Open(in); !errors.Is(err, ErrInvalidReceiptRMA) {
			t.Fatalf("Open = %v", err)
		}
	})
}

// The invariant: inspected qty <= received qty (and >= 1); the boundary
// (inspected == received) passes.
func TestInspectedQuantityWithinReceived(t *testing.T) {
	r := receipt(t)
	if err := r.Inspect(1, shared.GradeB, 2, []string{"CUSTOMER_DAMAGE"}, opened.Add(time.Hour)); err != nil {
		t.Fatalf("Inspect at the boundary = %v", err)
	}
	over := receipt(t)
	if err := over.Inspect(1, shared.GradeB, 3, nil, opened.Add(time.Hour)); !errors.Is(err, ErrInspectedExceedsReceived) {
		t.Fatalf("Inspect over = %v", err)
	}
	if err := over.Inspect(1, shared.GradeB, 0, nil, opened.Add(time.Hour)); !errors.Is(err, shared.ErrQuantityBelowOne) {
		t.Fatalf("Inspect 0 = %v", err)
	}
	line, err := over.Line(1)
	if err != nil || line.Inspected() {
		t.Fatalf("a refused inspect must not grade the line: %+v %v", line, err)
	}
}

// The invariant: a line is inspected exactly once.
func TestLineInspectedExactlyOnce(t *testing.T) {
	r := receipt(t)
	if err := r.Inspect(1, shared.GradeB, 2, []string{"CUSTOMER_DAMAGE"}, opened.Add(time.Hour)); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if err := r.Inspect(1, shared.GradeA, 2, nil, opened.Add(2*time.Hour)); !errors.Is(err, ErrLineAlreadyInspected) {
		t.Fatalf("second Inspect = %v, want ErrLineAlreadyInspected", err)
	}
	line, err := r.Line(1)
	if err != nil || !line.Inspected() || line.Grade() != shared.GradeB || line.InspectedQuantity() != 2 {
		t.Fatalf("line after inspect = %+v %v", line, err)
	}
	if got := line.ReasonCodes(); len(got) != 1 || got[0] != "CUSTOMER_DAMAGE" {
		t.Fatalf("reason codes = %v", got)
	}
}

// The inspection's effect: one ReturnLineInspected event and one version bump.
func TestInspectRaisesInspectedEvent(t *testing.T) {
	r := receipt(t)
	if err := r.Inspect(1, shared.GradeB, 2, []string{"CUSTOMER_DAMAGE"}, opened.Add(time.Hour)); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	evs := r.DomainEvents()
	if len(evs) != 3 { // opened + one unexpected + one inspection
		t.Fatalf("events = %d, want 3", len(evs))
	}
	insp, ok := evs[2].(ReturnLineInspected)
	if !ok || insp.LineNo != 1 || insp.SKU != "SKU-1" || insp.Quantity != 2 {
		t.Fatalf("inspection event = %+v", insp)
	}
	if insp.Grade != "B" || len(insp.ReasonCodes) != 1 || insp.ReasonCodes[0] != "CUSTOMER_DAMAGE" {
		t.Fatalf("inspection event = %+v", insp)
	}
	if r.Version() != 2 {
		t.Fatalf("version = %d, want 2", r.Version())
	}
}

// The failing paths of Inspect: unknown line, invalid grade, zero time.
func TestInspectValidation(t *testing.T) {
	r := receipt(t)
	if err := r.Inspect(9, shared.GradeA, 1, nil, opened); !errors.Is(err, ErrLineNotFound) {
		t.Fatalf("unknown line = %v", err)
	}
	if err := r.Inspect(1, shared.Grade("D"), 1, nil, opened); !errors.Is(err, ErrInvalidGrade) {
		t.Fatalf("bad grade = %v", err)
	}
	if err := r.Inspect(1, shared.GradeA, 1, nil, time.Time{}); !errors.Is(err, ErrNoTimestamp) {
		t.Fatalf("zero time = %v", err)
	}
	if _, err := r.Line(9); !errors.Is(err, ErrLineNotFound) {
		t.Fatalf("Line(9) = %v", err)
	}
	lines := r.Lines()
	if len(lines) != 2 || lines[0].Kind() != KindMatched || lines[1].Kind() != KindUnexpected {
		t.Fatalf("Lines() = %+v", lines)
	}
	if lines[1].RMAID() != "" || lines[0].RMAID() != "rma-1" {
		t.Fatalf("line rmas = %+v", lines)
	}
	if lines[0].Quantity() != 2 || lines[0].SKU() != "SKU-1" || lines[0].LineNo() != 1 {
		t.Fatalf("line fields = %+v", lines[0])
	}
}

// An unexpected-only receipt (no receipt-level rma) is valid and raises no
// matched facts.
func TestUnexpectedOnlyReceipt(t *testing.T) {
	r, err := Open(OpenInput{
		ReceiptID: "rcpt-2", SiteID: "WH1", At: opened,
		Lines: []Line{unexpectedLine(t)},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r.RMAID() != "" {
		t.Fatalf("rma = %q, want none", r.RMAID())
	}
	evs := r.DomainEvents()
	if len(evs) != 2 || evs[1].EventName() != "UnexpectedReturnReceived" {
		t.Fatalf("events = %v", evs)
	}
}

func receipt(t *testing.T) *Receipt {
	t.Helper()
	r, err := Open(OpenInput{
		ReceiptID: "rcpt-1", RMAID: "rma-1", SiteID: "WH1", At: opened,
		Lines: []Line{matchedLine(t), unexpectedLine(t)},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}
