package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// adviceFixture extends the memory fixture with the advice view of ADR 0012.
func adviceFixture(t *testing.T) (fixture, *memory.ReturnAdvices) {
	t.Helper()
	f := newFixture(t)
	f.declare(t)
	f.products.Put("sku-1", productClass([]string{"class:Standard"}))
	f.products.Put("sku-2", productClass([]string{"class:Standard"}))
	return f, memory.NewReturnAdvices()
}

// networkAdvice seeds one advice whose authorizationId is the rmaId the
// receipt flow is keyed on: sku-1 for seller-7 (3 units) and sku-2 for the
// operator (2 units).
func networkAdvice() views.ReturnAdvice {
	return views.ReturnAdvice{
		AuthorizationID: "ret-1001", OrderID: "ord-9", VendorID: "V-1", NodeID: "n-1",
		Disposition: "NEAREST_RETURNS_CENTER", Method: "DROP_OFF", State: views.AdviceStateAuthorized,
		Lines: []views.ReturnAdviceLine{
			{LineNo: 1, CatalogItemID: "sku-1", Quantity: 3, OwnerRef: "seller-7"},
			{LineNo: 2, CatalogItemID: "sku-2", Quantity: 2},
		},
		ReadyAt: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC),
	}
}

// networkReceipt opens a receipt keyed rmaId = authorizationId: two matched
// advice lines plus one unexpected sku the advice never authorized.
func networkReceipt(t *testing.T, f fixture) string {
	t.Helper()
	// The receipt is opened through the flow keyed rmaId = authorizationId
	// (POST /returns/{rmaId}/receipts with the advice's id in the path).
	uc := &usecases.OpenReturnReceipt{Receipts: f.receipts, Events: f.events, IDs: f.ids, Clock: f.clock()}
	r, err := uc.Execute(context.Background(), usecases.ReturnReceiptInput{
		RMAID:  "ret-1001",
		SiteID: "SITE1",
		Lines: []usecases.ReceiptLine{
			{LineNo: 1, SKU: "sku-1", Quantity: 3, RMAID: "ret-1001"},
			{LineNo: 2, SKU: "sku-2", Quantity: 1, RMAID: "ret-1001"},
			{LineNo: 3, SKU: "sku-9", Quantity: 1},
		},
	})
	if err != nil {
		t.Fatalf("open network receipt: %v", err)
	}
	return r.ID()
}

func (f fixture) networkInspectUC(advices *memory.ReturnAdvices) *usecases.InspectReturnLine {
	uc := f.inspectUC()
	uc.Advices, uc.AdviceFeed = advices, advices
	return uc
}

func TestInspectCompletion_ComposesOutcomeOnLastLine(t *testing.T) {
	ctx := context.Background()
	f, advices := adviceFixture(t)
	advices.ApplyReturnAdvice(ctx, networkAdvice())
	rc := networkReceipt(t, f)
	uc := f.networkInspectUC(advices)

	// Line 1 (grade A, RESTOCK) alone is not completion.
	if _, err := uc.Execute(ctx, usecases.Inspection{ReceiptID: rc, LineNo: 1, Grade: shared.GradeA, Quantity: 3}); err != nil {
		t.Fatal(err)
	}
	if pending, _ := advices.PendingOutcomes(ctx); len(pending) != 0 {
		t.Fatalf("outcome composed before the receipt completed: %+v", pending)
	}
	// Line 3 (grade SCRAP, DISPOSE) still not completion.
	if _, err := uc.Execute(ctx, usecases.Inspection{ReceiptID: rc, LineNo: 3, Grade: shared.GradeScrap, Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	if pending, _ := advices.PendingOutcomes(ctx); len(pending) != 0 {
		t.Fatalf("outcome composed before the receipt completed: %+v", pending)
	}
	// Line 2 (grade C, LIQUIDATE) completes the receipt: sku-1 restockable 3
	// (seller-7), sku-2 unsellable 1 (operator), sku-9 unsellable 1 (not
	// authorized — submitted anyway).
	if _, err := uc.Execute(ctx, usecases.Inspection{ReceiptID: rc, LineNo: 2, Grade: shared.GradeC, Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	pending, err := advices.PendingOutcomes(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v err=%v", pending, err)
	}
	assertComposedOutcome(t, pending[0].Outcome)
}

func assertComposedOutcome(t *testing.T, out views.ReturnOutcome) {
	t.Helper()
	if out.AuthorizationID != "ret-1001" || out.NodeID != "n-1" {
		t.Fatalf("outcome ids = %+v", out)
	}
	if out.ReceivedAt.IsZero() {
		t.Fatal("receivedAt lost")
	}
	want := map[string]views.ReturnOutcomeLine{
		"sku-1\x00seller-7": {CatalogItemID: "sku-1", OwnerRef: "seller-7", QtyRestockable: 3},
		"sku-2\x00":         {CatalogItemID: "sku-2", QtyUnsellable: 1},
		"sku-9\x00":         {CatalogItemID: "sku-9", QtyUnsellable: 1},
	}
	if len(out.Lines) != len(want) {
		t.Fatalf("lines = %+v", out.Lines)
	}
	for _, l := range out.Lines {
		key := l.CatalogItemID + "\x00" + l.OwnerRef
		w, ok := want[key]
		if !ok || l.QtyRestockable != w.QtyRestockable || l.QtyUnsellable != w.QtyUnsellable {
			t.Fatalf("line %+v not in %v", l, want)
		}
	}
}

func TestInspectCompletion_OutcomeStoredExactlyOnce(t *testing.T) {
	ctx := context.Background()
	f, advices := adviceFixture(t)
	advices.ApplyReturnAdvice(ctx, networkAdvice())
	rc := networkReceipt(t, f)
	uc := f.networkInspectUC(advices)
	for i, in := range []usecases.Inspection{
		{ReceiptID: rc, LineNo: 1, Grade: shared.GradeA, Quantity: 3},
		{ReceiptID: rc, LineNo: 2, Grade: shared.GradeB, Quantity: 1},
		{ReceiptID: rc, LineNo: 3, Grade: shared.GradeB, Quantity: 1},
	} {
		if _, err := uc.Execute(ctx, in); err != nil {
			t.Fatalf("inspect %d: %v", i, err)
		}
	}
	// A SECOND fully inspected receipt against the same advice must not
	// replace the stored outcome (the once-rule of ADR 0012): no error, no
	// second pending outcome.
	secondUC := &usecases.OpenReturnReceipt{Receipts: f.receipts, Events: f.events, IDs: f.ids, Clock: f.clock()}
	second, err := secondUC.Execute(ctx, usecases.ReturnReceiptInput{
		RMAID: "ret-1001", SiteID: "SITE1",
		Lines: []usecases.ReceiptLine{{LineNo: 1, SKU: "sku-1", Quantity: 1, RMAID: "ret-1001"}},
	})
	if err != nil {
		t.Fatalf("open second receipt: %v", err)
	}
	if _, err := uc.Execute(ctx, usecases.Inspection{ReceiptID: second.ID(), LineNo: 1, Grade: shared.GradeA, Quantity: 1}); err != nil {
		t.Fatalf("second receipt: %v", err)
	}
	pending, err := advices.PendingOutcomes(ctx)
	if err != nil || len(pending) != 1 || pending[0].Outcome.AuthorizationID != "ret-1001" {
		t.Fatalf("pending = %+v err=%v", pending, err)
	}
}

func TestInspectCompletion_NonNetworkReceiptIsUntouched(t *testing.T) {
	ctx := context.Background()
	f, advices := adviceFixture(t)
	advices.ApplyReturnAdvice(ctx, networkAdvice())
	uc := f.networkInspectUC(advices)
	// rma-1 is a plain fleet RMA, not an advice: the hook stays silent.
	rc := f.inspectedReceipt(t) // one matched line, rma-1
	if _, err := uc.Execute(ctx, usecases.Inspection{ReceiptID: rc, LineNo: 1, Grade: shared.GradeA, Quantity: 2}); err != nil {
		t.Fatal(err)
	}
	if pending, _ := advices.PendingOutcomes(ctx); len(pending) != 0 {
		t.Fatalf("non-network receipt composed an outcome: %+v", pending)
	}
}
