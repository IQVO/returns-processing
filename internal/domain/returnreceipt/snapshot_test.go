package returnreceipt

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

var snapAt = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

// openedSnapshot builds the snapshot of a two-line receipt (matched +
// unexpected) at version 1, optionally with the matched line inspected.
func openedSnapshot(t *testing.T, inspect bool) Snapshot {
	t.Helper()
	m, err := MatchedLine(1, "SKU-1", 4, "rma-1")
	if err != nil {
		t.Fatal(err)
	}
	u, err := UnexpectedLine(2, "SKU-2", 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(OpenInput{
		ReceiptID: "receipt-1", RMAID: "rma-1", SiteID: "site-1",
		Lines: []Line{m, u}, At: snapAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspect {
		if err := r.Inspect(1, shared.GradeA, 3, []string{"PACKAGING"}, snapAt.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	return r.Snapshot()
}

func TestSnapshot_RoundTrip(t *testing.T) {
	s := openedSnapshot(t, false)
	got, err := Restore(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version() != 1 {
		t.Fatalf("version = %d, want 1", got.Version())
	}
	assertNoPendingEvents(t, got.DomainEvents())
	assertResnapshotEqual(t, got.Snapshot(), s, "opened")
	// the restored uninspected line can still be inspected
	if err := got.Inspect(1, shared.GradeB, 1, nil, snapAt.Add(time.Hour)); err != nil {
		t.Fatalf("inspect restored line: %v", err)
	}
}

func TestSnapshot_RoundTripInspected(t *testing.T) {
	s := openedSnapshot(t, true)
	got, err := Restore(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version() != 2 {
		t.Fatalf("version = %d, want 2", got.Version())
	}
	assertNoPendingEvents(t, got.DomainEvents())
	assertResnapshotEqual(t, got.Snapshot(), s, "inspected")
	line, err := got.Line(1)
	if err != nil {
		t.Fatal(err)
	}
	if !line.Inspected() || line.RMAID() != "rma-1" {
		t.Fatalf("line 1 = inspected %t rma %s", line.Inspected(), line.RMAID())
	}
	if line.Grade() != shared.GradeA || line.InspectedQuantity() != 3 {
		t.Fatalf("inspection did not survive: %+v", line)
	}
	// the restored line refuses a second inspection
	if err := got.Inspect(1, shared.GradeB, 1, nil, snapAt); !errors.Is(err, ErrLineAlreadyInspected) {
		t.Fatalf("re-inspect = %v", err)
	}
}

func assertNoPendingEvents(t *testing.T, events []Event) {
	t.Helper()
	if len(events) != 0 {
		t.Fatalf("a restored receipt has no pending events, got %v", events)
	}
}

func assertResnapshotEqual(t *testing.T, got, want Snapshot, name string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: re-snapshot changed the state", name)
	}
}

func TestRestore_Refuses(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Snapshot)
		want error
	}{
		{"blank id", func(s *Snapshot) { s.ID = "" }, shared.ErrBlankID},
		{"blank site", func(s *Snapshot) { s.SiteID = "" }, shared.ErrBlankID},
		{"zero opened at", func(s *Snapshot) { s.OpenedAt = time.Time{} }, ErrNoTimestamp},
		{"no lines", func(s *Snapshot) { s.Lines = nil }, ErrNoLines},
		{"zero version", func(s *Snapshot) { s.Version = 0 }, ErrNoTimestamp},
		{"bad receipt rma", func(s *Snapshot) { s.RMAID = " " }, ErrInvalidReceiptRMA},
		{"line blank sku", func(s *Snapshot) { s.Lines[0].SKU = "" }, shared.ErrBlankID},
		{"line quantity below one", func(s *Snapshot) { s.Lines[0].Quantity = 0 }, shared.ErrQuantityBelowOne},
		{"line no below one", func(s *Snapshot) { s.Lines[0].LineNo = 0 }, shared.ErrLineNoBelowOne},
		{"unknown kind", func(s *Snapshot) { s.Lines[0].Kind = "OTHER" }, ErrLineKindMissing},
		{"matched without rma", func(s *Snapshot) { s.Lines[0].RMAID = "" }, ErrMatchedLineWithoutRMA},
		{"unexpected with rma", func(s *Snapshot) { s.Lines[1].RMAID = "rma-9" }, ErrUnexpectedLineWithRMA},
		{"duplicate line no", func(s *Snapshot) { s.Lines[1].LineNo = 1 }, ErrDuplicateLineNo},
		{"bad inspected grade", func(s *Snapshot) { s.Lines[0].Grade = "D" }, ErrInvalidGrade},
		{"inspected qty below one", func(s *Snapshot) { s.Lines[0].InspectedQuantity = 0 }, shared.ErrQuantityBelowOne},
		{"inspected above received", func(s *Snapshot) { s.Lines[0].InspectedQuantity = 9 }, ErrInspectedExceedsReceived},
		{"zero inspected at", func(s *Snapshot) { s.Lines[0].InspectedAt = time.Time{} }, ErrNoTimestamp},
		{"grade on uninspected", func(s *Snapshot) {
			s.Lines[1].Grade = shared.GradeA
		}, ErrLineAlreadyInspected},
		{"inspection stamp on uninspected", func(s *Snapshot) {
			s.Lines[1].InspectedAt = snapAt
		}, ErrLineAlreadyInspected},
		{"inspected qty on uninspected", func(s *Snapshot) {
			s.Lines[1].InspectedQuantity = 1
		}, ErrLineAlreadyInspected},
	}
	for _, c := range cases {
		s := openedSnapshot(t, true)
		c.mut(&s)
		if _, err := Restore(s); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}
