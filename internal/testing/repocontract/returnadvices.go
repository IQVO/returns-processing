package repocontract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// fixtureOwner is the stock owner of the fixture advice's line (goconst:
// shared by the advice and the outcome fixtures).
const fixtureOwner = "seller-7"

// fixtureAdvice is the advice every seed starts from; a case that cares
// passes its own value.
func fixtureAdvice(id string) views.ReturnAdvice {
	return views.ReturnAdvice{
		AuthorizationID: id,
		OrderID:         "ord-1",
		GroupNo:         2,
		VendorID:        "V-1",
		NodeID:          "n-1",
		Disposition:     "NEAREST_RETURNS_CENTER",
		Method:          "DROP_OFF",
		State:           views.AdviceStateAuthorized,
		Lines:           []views.ReturnAdviceLine{{LineNo: 1, CatalogItemID: fixtureSKU, Quantity: 2, OwnerRef: fixtureOwner}},
		TrackingNumbers: []string{"TRK-1"},
		ReadyAt:         time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC),
		ExpiresAt:       time.Date(2026, 12, 14, 10, 0, 0, 0, time.UTC),
	}
}

func fixtureOutcome(id string) views.ReturnOutcome {
	return views.ReturnOutcome{
		AuthorizationID: id,
		NodeID:          "n-1",
		ReceivedAt:      time.Date(2026, 11, 14, 12, 30, 0, 0, time.UTC),
		Lines:           []views.ReturnOutcomeLine{{CatalogItemID: fixtureSKU, OwnerRef: fixtureOwner, QtyRestockable: 2}},
	}
}

// ReturnAdviceStore is the combined advice-view port pair plus the seed the
// suites need (the poller's upsert).
type ReturnAdviceStore interface {
	ports.ReturnAdviceFeed
	ports.ReturnAdviceViews
}

// ReturnAdvices runs the advice-view contract on both adapters (ADR 0012).
func ReturnAdvices(t *testing.T, newStore func(t *testing.T) ReturnAdviceStore) {
	t.Helper()
	run(t, newStore, map[string]func(t *testing.T, s ReturnAdviceStore){
		"an unseen advice reads as missing":                  adviceMiss,
		"first sight persists the advice":                    adviceFirstSight,
		"re-delivery refreshes facts, not outcomes":          adviceRedelivery,
		"a pending outcome needs a seen advice":              advicePendingUnknown,
		"an outcome is stored exactly once":                  advicePendingOnce,
		"the drain lists pending and submitted oldest first": adviceDrain,
		"the watermark is the latest readyAt":                adviceWatermark,
	})
}

func adviceMiss(t *testing.T, s ReturnAdviceStore) {
	if _, ok, err := s.Get(context.Background(), "ret-none"); err != nil || ok {
		t.Fatalf("get = ok=%t err=%v", ok, err)
	}
}

func adviceFirstSight(t *testing.T, s ReturnAdviceStore) {
	a := fixtureAdvice("ret-1")
	must(t, s.ApplyReturnAdvice(context.Background(), a))
	got, ok, err := s.Get(context.Background(), "ret-1")
	if err != nil || !ok {
		t.Fatalf("get = ok=%t err=%v", ok, err)
	}
	if got.OutcomeState != "" || got.FirstSeenAt.IsZero() || len(got.Lines) != 1 || got.Lines[0].OwnerRef != "seller-7" {
		t.Fatalf("advice = %+v", got)
	}
}

func adviceRedelivery(t *testing.T, s ReturnAdviceStore) {
	ctx := context.Background()
	must(t, s.ApplyReturnAdvice(ctx, fixtureAdvice("ret-1")))
	must(t, s.MarkOutcomePending(ctx, "ret-1", fixtureOutcome("ret-1")))
	// A re-delivery carries a new tracking number and the same facts; the
	// outcome columns must survive it untouched.
	again := fixtureAdvice("ret-1")
	again.TrackingNumbers = []string{"TRK-1", "TRK-2"}
	must(t, s.ApplyReturnAdvice(ctx, again))
	got, ok, _ := s.Get(ctx, "ret-1")
	if !ok || got.OutcomeState != views.AdviceOutcomePending || len(got.TrackingNumbers) != 2 {
		t.Fatalf("advice = %+v", got)
	}
	pending, err := s.PendingOutcomes(ctx)
	if err != nil || len(pending) != 1 || pending[0].Outcome.AuthorizationID != "ret-1" {
		t.Fatalf("pending = %+v err=%v", pending, err)
	}
	// A succeeded transaction settles the outcome: terminal, idempotent.
	must(t, s.MarkOutcomeSucceeded(ctx, "ret-1"))
	if err != nil {
		t.Fatalf("succeed: %v", err)
	}
	must(t, s.MarkOutcomeSucceeded(ctx, "ret-1")) // a replayed verdict is a no-op
	if pending, err := s.PendingOutcomes(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pending after success = %+v err=%v", pending, err)
	}
	got, _, _ = s.Get(ctx, "ret-1")
	if got.OutcomeState != views.AdviceOutcomeSucceeded {
		t.Fatalf("succeeded advice = %+v", got)
	}
}

func advicePendingUnknown(t *testing.T, s ReturnAdviceStore) {
	err := s.MarkOutcomePending(context.Background(), "ret-none", fixtureOutcome("ret-none"))
	if err == nil || !errors.Is(err, ports.ErrAdviceUnknown) {
		t.Fatalf("pending unknown = %v", err)
	}
}

func advicePendingOnce(t *testing.T, s ReturnAdviceStore) {
	ctx := context.Background()
	must(t, s.ApplyReturnAdvice(ctx, fixtureAdvice("ret-1")))
	if err := s.MarkOutcomePending(ctx, "ret-1", fixtureOutcome("ret-1")); err != nil {
		t.Fatalf("pending: %v", err)
	}
	err := s.MarkOutcomePending(ctx, "ret-1", fixtureOutcome("ret-1"))
	if err == nil || !errors.Is(err, ports.ErrOutcomeAlreadyStored) {
		t.Fatalf("pending twice = %v", err)
	}
}

func adviceDrain(t *testing.T, s ReturnAdviceStore) {
	ctx := context.Background()
	for _, id := range []string{"ret-1", "ret-2"} {
		must(t, s.ApplyReturnAdvice(ctx, fixtureAdvice(id)))
		must(t, s.MarkOutcomePending(ctx, id, fixtureOutcome(id)))
	}
	// ret-1 is accepted by the network; ret-3 is refused terminally; ret-2
	// stays PENDING. Only ret-1 (settling) and ret-2 (submitting) drain.
	must(t, s.MarkOutcomeSubmitted(ctx, "ret-1", "tx-1"))
	must(t, s.ApplyReturnAdvice(ctx, fixtureAdvice("ret-3")))
	must(t, s.MarkOutcomePending(ctx, "ret-3", fixtureOutcome("ret-3")))
	must(t, s.MarkOutcomeFailed(ctx, "ret-3", "refused"))
	pending, err := s.PendingOutcomes(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %+v (want ret-1 and ret-2 only)", pending)
	}
	ids := map[string]string{}
	for _, p := range pending {
		ids[p.Outcome.AuthorizationID] = p.TransactionID
	}
	if ids["ret-1"] != "tx-1" || ids["ret-2"] != "" {
		t.Fatalf("pending = %+v", pending)
	}
	got, _, _ := s.Get(ctx, "ret-3")
	if got.OutcomeState != views.AdviceOutcomeFailed {
		t.Fatalf("failed advice = %+v", got)
	}
}

func adviceWatermark(t *testing.T, s ReturnAdviceStore) {
	ctx := context.Background()
	if _, ok, err := s.LatestReadyAt(ctx); err != nil || ok {
		t.Fatalf("empty watermark = ok=%t err=%v", ok, err)
	}
	first := fixtureAdvice("ret-1")
	second := fixtureAdvice("ret-2")
	second.ReadyAt = first.ReadyAt.Add(time.Hour)
	must(t, s.ApplyReturnAdvice(ctx, first))
	must(t, s.ApplyReturnAdvice(ctx, second))
	latest, ok, err := s.LatestReadyAt(ctx)
	if err != nil || !ok || !latest.Equal(second.ReadyAt) {
		t.Fatalf("watermark = %v ok=%t err=%v", latest, ok, err)
	}
}
