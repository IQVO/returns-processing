package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

var now = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

// authorized builds an AUTHORIZED RMA the way the use case would.
func authorized(t *testing.T, id, orderID string, lineNo int32, qty int64) *returnauthorization.RMA {
	t.Helper()
	rma, err := returnauthorization.Request(returnauthorization.RequestInput{
		RMAID: id, OrderID: orderID, LineNo: lineNo, SKU: "sku-1",
		Quantity: qty, SiteID: "SITE1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rma.Authorize(returnauthorization.Facts{
		ManifestedWorkUnit: rma.Line().WorkUnit(),
		PolicyMaxUnits:     5,
		WindowEndsAt:       now.Add(24 * time.Hour),
		Now:                now,
	}); err != nil {
		t.Fatal(err)
	}
	return rma
}

// TestClone_RoundTrip is the memory double's core contract, the in-memory
// stand-in for Postgres row rehydration: a read-back aggregate carries the
// full persisted state, has NO pending domain events (a live-pointer map
// would replay them on the next save), and shares no memory with the stored
// copy (caller mutations never corrupt the store).
func TestClone_RoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewRMAs()
	if err := repo.Save(ctx, authorized(t, "rma-1", "ord-1", 2, 3)); err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindByID(ctx, "rma-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != "rma-1" || got.Status() != returnauthorization.StatusAuthorized ||
		got.Version() != 1 || got.Quantity() != 3 || got.SKU() != "sku-1" {
		t.Fatalf("state lost in the round-trip: %+v", got)
	}
	if !got.WindowEndsAt().Equal(now.Add(24 * time.Hour).UTC()) {
		t.Fatalf("window lost: %v", got.WindowEndsAt())
	}
	if events := got.DomainEvents(); len(events) != 0 {
		t.Fatalf("read-back RMA replays published events: %v", eventNames(events))
	}
	// The returned copy shares nothing with the store: mutating it (without
	// saving) must not change what the repo holds.
	if err := got.MarkReceived(now); err != nil {
		t.Fatal(err)
	}
	again, err := repo.FindByID(ctx, "rma-1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Status() != returnauthorization.StatusAuthorized {
		t.Fatalf("caller mutation leaked into the store: %s", again.Status())
	}
}

// TestRMAs_OpenByOrderLineAndList proves the one-open-RMA query skips
// decided-terminal RMAs and ListByOrder orders by line.
func TestRMAs_OpenByOrderLineAndList(t *testing.T) {
	ctx := context.Background()
	repo := NewRMAs()
	open := authorized(t, "rma-open", "ord-1", 2, 3)
	if err := repo.Save(ctx, open); err != nil {
		t.Fatal(err)
	}
	rejected, err := returnauthorization.Request(returnauthorization.RequestInput{
		RMAID: "rma-rej", OrderID: "ord-1", LineNo: 4, SKU: "sku-1",
		Quantity: 1, SiteID: "SITE1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rejected.Reject(returnauthorization.RejectOpenRMAExists, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, rejected); err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindOpenByOrderLine(ctx, "ord-1", 2)
	if err != nil || got.ID() != "rma-open" {
		t.Fatalf("open lookup err=%v id=%s", err, got.ID())
	}
	if _, err := repo.FindOpenByOrderLine(ctx, "ord-1", 4); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("terminal RMA still open: %v", err)
	}
	if _, err := repo.FindOpenByOrderLine(ctx, "ord-2", 2); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("other order err = %v", err)
	}
	list, err := repo.ListByOrder(ctx, "ord-1", nil, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list err=%v len=%d", err, len(list))
	}
	if list[0].ID() != "rma-open" || list[1].ID() != "rma-rej" {
		t.Fatalf("list not ordered by line: %s, %s", list[0].ID(), list[1].ID())
	}
	if _, err := repo.FindByID(ctx, "nope"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing RMA err = %v", err)
	}
}

// TestReceipts_InspectAcrossSaves proves the replay bug stays dead across
// the receipt's own write path: open (v1), save; inspect line 1, save; the
// reloaded receipt keeps the inspection and carries no old facts.
func TestReceipts_InspectAcrossSaves(t *testing.T) {
	ctx := context.Background()
	repo := NewReceipts()
	matched, err := returnreceipt.MatchedLine(1, "sku-1", 3, "rma-1")
	if err != nil {
		t.Fatal(err)
	}
	unexpected, err := returnreceipt.UnexpectedLine(2, "sku-2", 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := returnreceipt.Open(returnreceipt.OpenInput{
		ReceiptID: "rc-1", SiteID: "SITE1", Lines: []returnreceipt.Line{matched, unexpected}, At: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.FindByID(ctx, "rc-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.DomainEvents()) != 0 {
		t.Fatal("read-back receipt replays the opened facts")
	}
	if err := loaded.Inspect(1, shared.GradeA, 2, []string{"torn-box"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	assertInspectedOnce(t, repo)
	assertUnsavedMutationsStayLocal(t, repo)
}

func assertInspectedOnce(t *testing.T, repo *Receipts) {
	t.Helper()
	reloaded, err := repo.FindByID(context.Background(), "rc-1")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Version() != 2 {
		t.Fatalf("version = %d, want 2", reloaded.Version())
	}
	line, err := reloaded.Line(1)
	if err != nil || !line.Inspected() || line.Grade() != shared.GradeA || line.InspectedQuantity() != 2 {
		t.Fatalf("inspection lost: inspected=%v grade=%s qty=%d err=%v", line.Inspected(), line.Grade(), line.InspectedQuantity(), err)
	}
	if reasons := line.ReasonCodes(); len(reasons) != 1 || reasons[0] != "torn-box" {
		t.Fatalf("reasons = %v", reasons)
	}
	if events := reloaded.DomainEvents(); len(events) != 0 {
		t.Fatalf("events not consumed by Save: %+v", events)
	}
}

// assertUnsavedMutationsStayLocal proves a loaded copy shares no memory
// with the store: mutating it without saving changes nothing persisted.
func assertUnsavedMutationsStayLocal(t *testing.T, repo *Receipts) {
	t.Helper()
	stray, err := repo.FindByID(context.Background(), "rc-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := stray.Inspect(2, shared.GradeB, 1, nil, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	kept, err := repo.FindByID(context.Background(), "rc-1")
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := kept.Line(2); l.Inspected() {
		t.Fatal("unsaved mutation leaked into the store (shared lines slice)")
	}
	if _, err := repo.FindByID(context.Background(), "nope"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing receipt err = %v", err)
	}
}

// decided builds one policy decision for (receipt, line).
func decided(t *testing.T, id, receiptID string, lineNo int32) *disposition.Disposition {
	t.Helper()
	policy := totalPolicy(t)
	d, err := disposition.DecideForLine(disposition.LineDecision{
		DispositionID: id, ReceiptID: receiptID, LineNo: lineNo,
		SKU: "sku-1", Quantity: 2, Grade: shared.GradeA,
		Policy: policy, SiteID: "SITE1", ReceivedAt: now, DecidedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func totalPolicy(t *testing.T) *disposition.DispositionPolicy {
	t.Helper()
	p, err := disposition.Declare(disposition.PolicyInput{
		Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
		Rules: []disposition.Rule{
			{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
			{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
		DeclaredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDispositions_LatestPerLine proves FindByReceiptLine returns the
// superseding decision, and the read-back copy has no pending facts.
func TestDispositions_LatestPerLine(t *testing.T) {
	ctx := context.Background()
	repo := NewDispositions()
	orig := decided(t, "disp-1", "rc-1", 1)
	if err := repo.Save(ctx, orig); err != nil {
		t.Fatal(err)
	}
	over, err := orig.Override(disposition.OverrideInput{
		NewDispositionID: "disp-2", Outcome: disposition.OutcomeRTV, Reason: "vendor-recall", DecidedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, over); err != nil {
		t.Fatal(err)
	}
	latest, err := repo.FindByReceiptLine(ctx, "rc-1", 1)
	if err != nil || latest.ID() != "disp-2" || latest.Supersedes() != "disp-1" {
		t.Fatalf("latest err=%v id=%s supersedes=%s", err, latest.ID(), latest.Supersedes())
	}
	if events := latest.DomainEvents(); len(events) != 0 {
		t.Fatal("read-back disposition replays published events")
	}
	if _, err := repo.FindByReceiptLine(ctx, "rc-1", 9); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing line decision err = %v", err)
	}
	if _, err := repo.FindByID(ctx, "nope"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing disposition err = %v", err)
	}
}

// TestPolicies_Versions proves append-only versions and the current mark.
func TestPolicies_Versions(t *testing.T) {
	ctx := context.Background()
	repo := NewPolicies()
	if _, err := repo.Current(ctx); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("empty current err = %v", err)
	}
	if err := repo.Save(ctx, totalPolicy(t)); err != nil {
		t.Fatal(err)
	}
	v2, err := disposition.Declare(disposition.PolicyInput{
		Version: 2, ReturnWindowDays: 45, MaxUnitsPerLine: 6,
		Rules: []disposition.Rule{
			{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
			{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
		DeclaredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, v2); err != nil {
		t.Fatal(err)
	}
	cur, err := repo.Current(ctx)
	if err != nil || cur.Version() != 2 || cur.ReturnWindowDays() != 45 {
		t.Fatalf("current err=%v v=%d window=%d", err, cur.Version(), cur.ReturnWindowDays())
	}
	old, err := repo.FindByVersion(ctx, 1)
	if err != nil || old.Version() != 1 || old.ReturnWindowDays() != 30 {
		t.Fatalf("v1 err=%v v=%d window=%d", err, old.Version(), old.ReturnWindowDays())
	}
	if _, err := repo.FindByVersion(ctx, 9); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing version err = %v", err)
	}
}

func eventNames(events []returnauthorization.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.EventName())
	}
	return out
}
