package disposition

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

var snapAt = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

// decidedSnapshot builds a policy-decided RESTOCK disposition (staged).
func decidedSnapshot(t *testing.T) Snapshot {
	t.Helper()
	p := declaredPolicy(t)
	d, err := DecideForLine(LineDecision{
		DispositionID: "disp-1", ReceiptID: "receipt-1", LineNo: 1,
		SKU: "SKU-1", Quantity: 3, Grade: shared.GradeA,
		Classification: Classification{SKU: "SKU-1", ProductClass: "Standard", Classified: true},
		Policy:         p, SiteID: "site-1", RMAID: "rma-1",
		ReceivedAt: snapAt, DecidedAt: snapAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return d.Snapshot()
}

func declaredPolicy(t *testing.T) *DispositionPolicy {
	t.Helper()
	p, err := Declare(PolicyInput{
		Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
		Rules: []Rule{
			{Grade: shared.GradeA, ProductClass: "Standard", Outcome: OutcomeRestock},
			{Grade: shared.GradeA, ProductClass: "*", Outcome: OutcomeRefurbish},
			{Grade: shared.GradeB, ProductClass: "*", Outcome: OutcomeRefurbish},
			{Grade: shared.GradeC, ProductClass: "*", Outcome: OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: "*", Outcome: OutcomeDispose},
		},
		DeclaredAt: snapAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSnapshot_RoundTrip(t *testing.T) {
	s := decidedSnapshot(t)
	got, err := Restore(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome() != OutcomeRestock || !got.Staged() {
		t.Fatalf("got %s staged=%t", got.Outcome(), got.Staged())
	}
	if len(got.DomainEvents()) != 0 {
		t.Fatal("a restored disposition has no pending events")
	}
	if !reflect.DeepEqual(got.Snapshot(), s) {
		t.Fatalf("re-snapshot changed the state:\ngot  %+v\nwant %+v", got.Snapshot(), s)
	}
	// staging exactly once holds across the trip
	if err := got.Stage(); !errors.Is(err, ErrAlreadyStaged) {
		t.Fatalf("re-stage = %v", err)
	}

	// an override (supersedes set, reason carried) round trips too
	over, err := got.Override(OverrideInput{
		NewDispositionID: "disp-2", Outcome: OutcomeRTV, Reason: "customer request",
		DecidedAt: snapAt.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	os := over.Snapshot()
	og, err := Restore(os)
	if err != nil {
		t.Fatal(err)
	}
	if og.Supersedes() != "disp-1" || og.Reason() != "customer request" {
		t.Fatalf("override lost its supersedes/reason: %+v", og.Snapshot())
	}
	if !reflect.DeepEqual(og.Snapshot(), os) {
		t.Fatal("re-snapshot changed the override")
	}

	// a policy version round trips through its own snapshot
	ps := declaredPolicy(t).Snapshot()
	pg, err := RestorePolicy(ps)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pg.Snapshot(), ps) {
		t.Fatalf("policy re-snapshot changed:\ngot  %+v\nwant %+v", pg.Snapshot(), ps)
	}
}

func TestRestore_Refuses(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Snapshot)
		want error
	}{
		{"blank id", func(s *Snapshot) { s.ID = "" }, shared.ErrBlankID},
		{"blank receipt", func(s *Snapshot) { s.ReceiptID = "" }, shared.ErrBlankID},
		{"line no below one", func(s *Snapshot) { s.LineNo = 0 }, shared.ErrLineNoBelowOne},
		{"blank sku", func(s *Snapshot) { s.SKU = "" }, shared.ErrBlankID},
		{"quantity below one", func(s *Snapshot) { s.Quantity = 0 }, shared.ErrQuantityBelowOne},
		{"blank site", func(s *Snapshot) { s.SiteID = "" }, shared.ErrBlankID},
		{"bad rma id", func(s *Snapshot) { s.RMAID = " " }, shared.ErrBlankID},
		{"unknown grade", func(s *Snapshot) { s.Grade = "D" }, ErrInvalidGrade},
		{"unknown outcome", func(s *Snapshot) { s.Outcome = "BURN" }, ErrInvalidForcedOutcome},
		{"zero policy version", func(s *Snapshot) { s.PolicyVersion = 0 }, ErrInvalidPolicy},
		{"zero received at", func(s *Snapshot) { s.ReceivedAt = time.Time{} }, ErrNoTimestamp},
		{"zero decided at", func(s *Snapshot) { s.DecidedAt = time.Time{} }, ErrNoTimestamp},
		{"staged non-restock", func(s *Snapshot) { s.Outcome = OutcomeDispose }, ErrNotRestock},
		{"wrong version", func(s *Snapshot) { s.Version = 2 }, ErrInvalidPolicy},
		{"bad supersedes", func(s *Snapshot) { s.Supersedes = " " }, shared.ErrBlankID},
	}
	for _, c := range cases {
		s := decidedSnapshot(t)
		c.mut(&s)
		if _, err := Restore(s); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

func TestRestorePolicy_Refuses(t *testing.T) {
	base := declaredPolicy(t).Snapshot()
	cases := []struct {
		name string
		mut  func(*PolicySnapshot)
		want error
	}{
		{"zero version", func(s *PolicySnapshot) { s.Version = 0 }, ErrInvalidPolicy},
		{"negative window", func(s *PolicySnapshot) { s.ReturnWindowDays = -1 }, ErrInvalidPolicy},
		{"zero max units", func(s *PolicySnapshot) { s.MaxUnitsPerLine = 0 }, ErrInvalidPolicy},
		{"bad veto", func(s *PolicySnapshot) { s.VetoOutcome = "BURN" }, ErrInvalidVetoOutcome},
		{"zero declared at", func(s *PolicySnapshot) { s.DeclaredAt = time.Time{} }, ErrNoTimestamp},
		{"not total", func(s *PolicySnapshot) {
			s.Rules = s.Rules[:len(s.Rules)-1]
		}, ErrPolicyNotTotal},
	}
	for _, c := range cases {
		s := base
		c.mut(&s)
		if _, err := RestorePolicy(s); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}
