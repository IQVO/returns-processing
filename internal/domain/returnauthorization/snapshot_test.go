package returnauthorization

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

var snapAt = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

// decidedSnapshot builds the snapshot of an AUTHORIZED rma at version 1.
func decidedSnapshot() Snapshot {
	r, err := Request(RequestInput{
		RMAID: "rma-1", OrderID: "ord-1", LineNo: 2, SKU: "SKU-1",
		Quantity: 3, SiteID: "site-1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		panic(err)
	}
	if err := r.Authorize(Facts{
		ManifestedWorkUnit: "ord-1-line-2",
		PolicyMaxUnits:     5,
		WindowEndsAt:       snapAt.Add(30 * 24 * time.Hour),
		Now:                snapAt,
	}); err != nil {
		panic(err)
	}
	return r.Snapshot()
}

// checkRoundTrip restores s and asserts the state survived the trip unchanged.
func checkRoundTrip(t *testing.T, s Snapshot, wantStatus Status, wantVersion int64) {
	t.Helper()
	got, err := Restore(s)
	if err != nil {
		t.Fatalf("restore %s: %v", wantStatus, err)
	}
	if got.Status() != wantStatus || got.Version() != wantVersion {
		t.Fatalf("got %s/%d, want %s/%d", got.Status(), got.Version(), wantStatus, wantVersion)
	}
	if len(got.DomainEvents()) != 0 {
		t.Fatalf("a restored rma has no pending events, got %v", got.DomainEvents())
	}
	if again := got.Snapshot(); again != s {
		t.Fatalf("re-snapshot changed the state:\ngot  %+v\nwant %+v", again, s)
	}
}

func mustRestore(t *testing.T, s Snapshot) *RMA {
	t.Helper()
	r, err := Restore(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSnapshot_RoundTrip(t *testing.T) {
	// authorized v1 -> received v2 -> closed v3, each state persisted and
	// restored through its snapshot
	s := decidedSnapshot()
	checkRoundTrip(t, s, StatusAuthorized, 1)

	r := mustRestore(t, s)
	if err := r.MarkReceived(snapAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s = r.Snapshot()
	checkRoundTrip(t, s, StatusReceived, 2)

	r = mustRestore(t, s)
	if err := r.Close(snapAt.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	checkRoundTrip(t, r.Snapshot(), StatusClosed, 3)

	// a rejected rma round trips too
	rej, err := Request(RequestInput{
		RMAID: "rma-2", OrderID: "ord-1", LineNo: 3, SKU: "SKU-1",
		Quantity: 1, SiteID: "site-1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rej.Reject(RejectReturnWindowExpired, snapAt); err != nil {
		t.Fatal(err)
	}
	rs := rej.Snapshot()
	got := mustRestore(t, rs)
	if got.Status() != StatusRejected || got.RejectionCode() != RejectReturnWindowExpired {
		t.Fatalf("got %s/%s", got.Status(), got.RejectionCode())
	}
	if got.Snapshot() != rs {
		t.Fatal("re-snapshot changed the rejected state")
	}

	// a requested rma (version 0) round trips
	req, err := Request(RequestInput{
		RMAID: "rma-3", OrderID: "ord-1", LineNo: 4, SKU: "SKU-1",
		Quantity: 1, SiteID: "site-1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkRoundTrip(t, req.Snapshot(), StatusRequested, 0)
}

func TestRestore_Refuses(t *testing.T) {
	base := decidedSnapshot()
	cases := []struct {
		name string
		mut  func(*Snapshot)
		want error
	}{
		{"blank id", func(s *Snapshot) { s.ID = " " }, shared.ErrBlankID},
		{"blank order", func(s *Snapshot) { s.OrderID = "" }, shared.ErrBlankID},
		{"line no below one", func(s *Snapshot) { s.LineNo = 0 }, shared.ErrLineNoBelowOne},
		{"blank sku", func(s *Snapshot) { s.SKU = "" }, shared.ErrBlankID},
		{"quantity below one", func(s *Snapshot) { s.Quantity = 0 }, shared.ErrQuantityBelowOne},
		{"blank site", func(s *Snapshot) { s.SiteID = "" }, shared.ErrBlankID},
		{"blank reason", func(s *Snapshot) { s.ReasonCode = " " }, ErrBlankReasonCode},
		{"unknown status", func(s *Snapshot) { s.Status = "HELD" }, ErrInvalidStatus},
		{"rejection on authorized", func(s *Snapshot) { s.RejectionCode = RejectOpenRMAExists }, ErrInvalidSnapshot},
		{"no rejection on rejected", func(s *Snapshot) {
			s.Status = StatusRejected
		}, ErrInvalidSnapshot},
		{"unknown rejection code", func(s *Snapshot) {
			s.Status = StatusRejected
			s.RejectionCode = "NOPE"
		}, ErrInvalidRejectionCode},
		{"requested with version", func(s *Snapshot) {
			s.Status = StatusRequested
			s.Version = 2
		}, ErrInvalidSnapshot},
		{"decided without version", func(s *Snapshot) { s.Version = 0 }, ErrInvalidSnapshot},
	}
	for _, c := range cases {
		s := base
		c.mut(&s)
		if _, err := Restore(s); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}
