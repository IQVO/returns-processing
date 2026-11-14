package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
)

func adviceAt(id string, readyAt time.Time) views.ReturnAdvice {
	return views.ReturnAdvice{
		AuthorizationID: id, OrderID: "ord-1", VendorID: "V-1", NodeID: "n-1",
		Disposition: "NEAREST_RETURNS_CENTER", Method: "DROP_OFF", State: views.AdviceStateAuthorized,
		Lines:   []views.ReturnAdviceLine{{LineNo: 1, CatalogItemID: "sku-1", Quantity: 2, OwnerRef: "seller-7"}},
		ReadyAt: readyAt,
	}
}

func TestPollReturnAdvicesStoresFirstSightAndRedelivery(t *testing.T) {
	ctx := context.Background()
	store := memory.NewReturnAdvices()
	gw := &fakeReturnsGateway{advices: []views.ReturnAdvice{adviceAt("ret-1", time.Unix(1000, 0))}}
	uc := &usecases.PollReturnAdvices{Gateway: gw, Views: store, Feed: store}

	res, err := uc.Execute(ctx)
	if err != nil || res.Seen != 1 || res.Applied != 1 {
		t.Fatalf("poll = %+v err=%v", res, err)
	}
	gw.advices = append(gw.advices, adviceAt("ret-1", time.Unix(1000, 0)))
	again := adviceAt("ret-1", time.Unix(1000, 0))
	again.TrackingNumbers = []string{"TRK-2"}
	gw.advices = append(gw.advices, again)
	res, err = uc.Execute(ctx)
	if err != nil {
		t.Fatalf("re-poll: %v", err)
	}
	// three deliveries, one advice: Seen counts unique ids, Applied counts
	// upserts (a page overlap re-delivers; the newest delivery wins).
	if res.Seen != 1 || res.Applied != 3 {
		t.Fatalf("re-poll = %+v", res)
	}
	got, ok, _ := store.Get(ctx, "ret-1")
	if !ok || len(got.TrackingNumbers) != 1 || got.TrackingNumbers[0] != "TRK-2" {
		t.Fatalf("advice = %+v", got)
	}
	if got.OutcomeState != "" {
		t.Fatalf("poll must not touch outcome state, got %q", got.OutcomeState)
	}
}

func TestPollReturnAdvicesWatermarkOverlaps(t *testing.T) {
	ctx := context.Background()
	store := memory.NewReturnAdvices()
	ready := time.Unix(1_000_000, 0).UTC()
	gw := &fakeReturnsGateway{advices: []views.ReturnAdvice{adviceAt("ret-1", ready)}}
	uc := &usecases.PollReturnAdvices{Gateway: gw, Views: store, Feed: store}
	if _, err := uc.Execute(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if _, err := uc.Execute(ctx); err != nil {
		t.Fatalf("re-poll: %v", err)
	}
	// The second poll must look back 2 minutes before the watermark, so an
	// advice committed just after the previous poll began is still found.
	if len(gw.polled) != 2 {
		t.Fatalf("polls = %d", len(gw.polled))
	}
	want := ready.Add(-2 * time.Minute)
	if !gw.polled[1].Equal(want) {
		t.Fatalf("window = %v, want %v", gw.polled[1], want)
	}
}

func TestPollReturnAdvicesStopsAtFirstStoreFailure(t *testing.T) {
	ctx := context.Background()
	store := &failingAdviceStore{adviceStore: memory.NewReturnAdvices(), failID: "ret-2"}
	gw := &fakeReturnsGateway{advices: []views.ReturnAdvice{
		adviceAt("ret-1", time.Unix(1000, 0)), adviceAt("ret-2", time.Unix(2000, 0)),
	}}
	uc := &usecases.PollReturnAdvices{Gateway: gw, Views: store, Feed: store}
	res, err := uc.Execute(ctx)
	if err == nil || res.Applied != 1 {
		t.Fatalf("poll = %+v err=%v (the tick must stop before the watermark passes a failed upsert)", res, err)
	}
}

func TestSubmitReturnOutcomesDrainsPendingAndSettles(t *testing.T) {
	ctx := context.Background()
	store := memory.NewReturnAdvices()
	for _, id := range []string{"ret-1", "ret-2"} {
		store.ApplyReturnAdvice(ctx, adviceAt(id, time.Unix(1000, 0)))
		store.MarkOutcomePending(ctx, id, views.ReturnOutcome{AuthorizationID: id, NodeID: "n-1", ReceivedAt: time.Unix(3000, 0)})
	}
	gw := &fakeReturnsGateway{submitTx: "tx-1", txs: map[string]views.ReturnOutcomeTransaction{
		"tx-1": {Status: views.TransactionSucceeded},
	}}
	uc := &usecases.SubmitReturnOutcomes{Gateway: gw, Views: store, Feed: store}

	res, err := uc.Execute(ctx)
	if err != nil || res.Submitted != 2 {
		t.Fatalf("drain = %+v err=%v", res, err)
	}
	if len(gw.submits) != 2 {
		t.Fatalf("submits = %d", len(gw.submits))
	}
	// One step per tick: the second tick settles both transactions.
	res, err = uc.Execute(ctx)
	if err != nil || res.Settled != 2 {
		t.Fatalf("settle drain = %+v err=%v", res, err)
	}
	// Settled outcomes are terminal: the third tick does nothing.
	res, err = uc.Execute(ctx)
	if err != nil || res.Submitted != 0 || res.Settled != 0 {
		t.Fatalf("third drain = %+v err=%v", res, err)
	}
}

func TestSubmitReturnOutcomesRefusalIsTerminal(t *testing.T) {
	ctx := context.Background()
	store := memory.NewReturnAdvices()
	store.ApplyReturnAdvice(ctx, adviceAt("ret-1", time.Unix(1000, 0)))
	store.MarkOutcomePending(ctx, "ret-1", views.ReturnOutcome{AuthorizationID: "ret-1", NodeID: "n-1"})
	gw := &fakeReturnsGateway{submitErr: errRefusedOutcome}
	uc := &usecases.SubmitReturnOutcomes{Gateway: gw, Views: store, Feed: store}

	res, err := uc.Execute(ctx)
	if err != nil || res.Failed != 1 {
		t.Fatalf("drain = %+v err=%v", res, err)
	}
	got, _, _ := store.Get(ctx, "ret-1")
	if got.OutcomeState != views.AdviceOutcomeFailed {
		t.Fatalf("state = %q", got.OutcomeState)
	}
	// A FAILED outcome never drains again.
	res, err = uc.Execute(ctx)
	if err != nil || res.Failed != 0 || res.Submitted != 0 {
		t.Fatalf("second drain = %+v err=%v", res, err)
	}
}

func TestSubmitReturnOutcomesTransportErrorRetries(t *testing.T) {
	ctx := context.Background()
	store := memory.NewReturnAdvices()
	store.ApplyReturnAdvice(ctx, adviceAt("ret-1", time.Unix(1000, 0)))
	store.MarkOutcomePending(ctx, "ret-1", views.ReturnOutcome{AuthorizationID: "ret-1", NodeID: "n-1"})
	gw := &fakeReturnsGateway{submitErr: errors.New("connection refused"), txs: map[string]views.ReturnOutcomeTransaction{}}
	uc := &usecases.SubmitReturnOutcomes{Gateway: gw, Views: store, Feed: store}

	if _, err := uc.Execute(ctx); err == nil {
		t.Fatal("a transport error must surface (logged by the worker), not vanish")
	}
	got, _, _ := store.Get(ctx, "ret-1")
	if got.OutcomeState != views.AdviceOutcomePending {
		t.Fatalf("state = %q (a transport error must leave the outcome pending)", got.OutcomeState)
	}
	// The network heals: the next tick submits and settles.
	gw.submitErr, gw.submitTx = nil, "tx-9"
	gw.txs["tx-9"] = views.ReturnOutcomeTransaction{Status: views.TransactionSucceeded}
	if _, err := uc.Execute(ctx); err != nil {
		t.Fatalf("healed drain: %v", err)
	}
}

func TestSubmitReturnOutcomesFailedTransactionIsTerminal(t *testing.T) {
	ctx := context.Background()
	store := memory.NewReturnAdvices()
	store.ApplyReturnAdvice(ctx, adviceAt("ret-1", time.Unix(1000, 0)))
	store.MarkOutcomePending(ctx, "ret-1", views.ReturnOutcome{AuthorizationID: "ret-1", NodeID: "n-1"})
	gw := &fakeReturnsGateway{submitTx: "tx-1", txs: map[string]views.ReturnOutcomeTransaction{
		"tx-1": {Status: views.TransactionFailed, Errors: "node-mismatch: n-9"},
	}}
	uc := &usecases.SubmitReturnOutcomes{Gateway: gw, Views: store, Feed: store}

	res, err := uc.Execute(ctx)
	if err != nil || res.Submitted != 1 {
		t.Fatalf("drain = %+v err=%v", res, err)
	}
	// The failed verdict arrives with the settlement tick.
	res, err = uc.Execute(ctx)
	if err != nil || res.Failed != 1 {
		t.Fatalf("settle drain = %+v err=%v", res, err)
	}
	if got := store.Refusal("ret-1"); got != "node-mismatch: n-9" {
		t.Fatalf("refusal = %q", got)
	}
}
