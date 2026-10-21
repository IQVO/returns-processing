package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// fixture wires every use case against the memory doubles: the same wiring
// the BDD step will reuse, real ports all the way down.
type fixture struct {
	rmas       *memory.RMAs
	receipts   *memory.Receipts
	disps      *memory.Dispositions
	policies   *memory.Policies
	events     *memory.Events
	allocated  *memory.AllocatedLines
	manifested *memory.ManifestedUnits
	products   *memory.ProductClasses
	ids        *memory.IDGen
	now        time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	return fixture{
		rmas:       memory.NewRMAs(),
		receipts:   memory.NewReceipts(),
		disps:      memory.NewDispositions(),
		policies:   memory.NewPolicies(),
		events:     &memory.Events{},
		allocated:  memory.NewAllocatedLines(),
		manifested: memory.NewManifestedUnits(),
		products:   memory.NewProductClasses(),
		ids:        memory.NewIDGen(),
		now:        time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC),
	}
}

func (f fixture) clock() memory.Clock { return memory.Clock{T: f.now} }

// declare installs the standard total policy: every grade restocks-or-else
// under "*", A/B restock, C liquidates, SCRAP disposes, veto DISPOSE.
func (f fixture) declare(t *testing.T) {
	t.Helper()
	uc := &usecases.DeclareDispositionPolicy{Policies: f.policies, Clock: f.clock()}
	_, err := uc.Execute(context.Background(), usecases.PolicyDeclaration{
		Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
		Rules: []disposition.Rule{
			{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
			{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
	})
	if err != nil {
		t.Fatalf("declare policy: %v", err)
	}
}

// declareRestockAll installs a total table that restocks every grade: the
// fixture for guardrail-demotion checks.
func (f fixture) declareRestockAll(t *testing.T) {
	t.Helper()
	uc := &usecases.DeclareDispositionPolicy{Policies: f.policies, Clock: f.clock()}
	var rules []disposition.Rule
	for _, g := range shared.Grades {
		rules = append(rules, disposition.Rule{Grade: g, ProductClass: "*", Outcome: disposition.OutcomeRestock})
	}
	_, err := uc.Execute(context.Background(), usecases.PolicyDeclaration{
		Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5, Rules: rules,
	})
	if err != nil {
		t.Fatalf("declare restock-all policy: %v", err)
	}
}

func productClass(tags []string) views.ProductClass {
	return views.ProductClass{Tags: tags, TemperatureClass: "AMBIENT", Version: 1}
}

// seed copies a shipped order line: allocated (the sku truth) and
// manifested (it shipped in package pkg).
func (f fixture) seed(orderID string, lineNo int32, sku, pkg string) {
	f.allocated.Put(orderID, lineNo, views.AllocatedLine{SKU: sku})
	f.manifested.PutByOrderLine(orderID, lineNo, views.ManifestedUnit{PackageID: pkg, ManifestedAt: f.now})
}

// authorize is the happy path AuthorizeReturn: a shipped line, a policy,
// and a request within the cap.
func (f fixture) authorize(t *testing.T, orderID string, lineNo int32, qty int64) *returnauthorization.RMA {
	t.Helper()
	f.declare(t)
	f.seed(orderID, lineNo, "sku-1", "pkg-1")
	uc := &usecases.AuthorizeReturn{
		RMAs: f.rmas, Policies: f.policies, Allocated: f.allocated,
		Manifested: f.manifested, Events: f.events, IDs: f.ids, Clock: f.clock(),
	}
	rma, err := uc.Execute(context.Background(), usecases.ReturnRequest{
		OrderID: orderID, LineNo: lineNo, Quantity: qty, SiteID: "SITE1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	return rma
}

// openReceipt opens a receipt whose lines carry the fixture's sku.
func (f fixture) openReceipt(t *testing.T, lines ...usecases.ReceiptLine) *returnreceipt.Receipt {
	t.Helper()
	uc := &usecases.OpenReturnReceipt{
		Receipts: f.receipts, Events: f.events, IDs: f.ids, Clock: f.clock(),
	}
	r, err := uc.Execute(context.Background(), usecases.ReturnReceiptInput{SiteID: "SITE1", Lines: lines})
	if err != nil {
		t.Fatalf("open receipt: %v", err)
	}
	return r
}

func (f fixture) matchedLine(lineNo int32, qty int64, rmaID string) usecases.ReceiptLine {
	return usecases.ReceiptLine{LineNo: lineNo, SKU: "sku-1", Quantity: qty, RMAID: rmaID}
}
