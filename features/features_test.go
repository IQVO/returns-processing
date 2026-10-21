// Package features_test hosts the godog (Cucumber for Go) acceptance suite
// next to the .feature files it runs (no Go file lives at the repo root).
// It drives the REAL use cases (the application layer) over the in-memory
// doubles, wired the way cmd/api will wire them over Postgres and Kafka;
// later steps of the service put the HTTP router and the Kafka consumers
// in front of the same world. The storage invariants the doubles enforce
// are the ones the Postgres adapters prove in the repocontract suite.
package features_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
)

// TestFeatures runs every Gherkin feature in this directory.
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"."},
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

// wall is the scenario's mutable service clock: steps set and advance it,
// every use case stamps with it.
type wall struct{ now time.Time }

func (c *wall) Now() time.Time { return c.now }

// app is the set of use cases of one scenario.
type app struct {
	declare   *usecases.DeclareDispositionPolicy
	authorize *usecases.AuthorizeReturn
	open      *usecases.OpenReturnReceipt
	inspect   *usecases.InspectReturnLine
	override  *usecases.OverrideDisposition
	getReturn *usecases.GetReturn
	list      *usecases.ListReturnsByOrder
	policy    *usecases.GetDispositionPolicy
}

// world is the per-scenario state.
type world struct {
	uc        app
	receipts  *memory.Receipts
	disps     *memory.Dispositions
	ev        *memory.Events
	alloc     *memory.AllocatedLines
	manif     *memory.ManifestedUnits
	prods     *memory.ProductClasses
	clock     *wall
	ctx       context.Context
	rmaID     string
	receiptID string

	lastErr    error
	errChecked bool
	unchecked  []error
}

// start builds the application over fresh in-memory doubles, as cmd/api
// does over Postgres (nil UnitOfWork = the memory wiring runs unwrapped).
// Every use case shares the SAME doubles: one repo per aggregate, one
// event log, one id generator, one clock.
func (w *world) start() {
	rmas, receipts := memory.NewRMAs(), memory.NewReceipts()
	disps, policies := memory.NewDispositions(), memory.NewPolicies()
	ev := &memory.Events{}
	alloc, manif := memory.NewAllocatedLines(), memory.NewManifestedUnits()
	prods := memory.NewProductClasses()
	ids, clock := memory.NewIDGen(), &wall{now: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}
	w.receipts, w.disps, w.ev = receipts, disps, ev
	w.alloc, w.manif, w.prods, w.clock = alloc, manif, prods, clock
	w.uc = app{
		declare:   &usecases.DeclareDispositionPolicy{Policies: policies, Clock: clock},
		authorize: &usecases.AuthorizeReturn{RMAs: rmas, Policies: policies, Allocated: alloc, Manifested: manif, Events: ev, IDs: ids, Clock: clock},
		open:      &usecases.OpenReturnReceipt{Receipts: receipts, Events: ev, IDs: ids, Clock: clock},
		inspect:   &usecases.InspectReturnLine{Receipts: receipts, Dispositions: disps, Policies: policies, Products: prods, Events: ev, IDs: ids, Clock: clock},
		override:  &usecases.OverrideDisposition{Dispositions: disps, Events: ev, IDs: ids, Clock: clock},
		getReturn: &usecases.GetReturn{RMAs: rmas},
		list:      &usecases.ListReturnsByOrder{RMAs: rmas},
		policy:    &usecases.GetDispositionPolicy{Policies: policies},
	}
	w.ctx = context.Background()
	w.rmaID, w.receiptID = "", ""
	w.lastErr, w.errChecked, w.unchecked = nil, false, nil
}

// run starts one command: a failure of the previous one that no step looked
// at is remembered and fails the scenario at the end.
func (w *world) run(fn func() error) {
	if w.lastErr != nil && !w.errChecked {
		w.unchecked = append(w.unchecked, w.lastErr)
	}
	w.lastErr, w.errChecked = fn(), false
}

// currentReturn re-reads the scenario's RMA through the read use case, so
// assertions see the PERSISTED state, not a stale pointer.
func (w *world) currentReturn() views.ReturnView {
	v, err := w.uc.getReturn.Execute(w.ctx, w.rmaID)
	if err != nil {
		panic(fmt.Sprintf("rma %s vanished: %v", w.rmaID, err))
	}
	return v
}

// currentReceipt re-reads the scenario's receipt from the repository.
func (w *world) currentReceipt() *returnreceipt.Receipt {
	r, err := w.receipts.FindByID(w.ctx, w.receiptID)
	if err != nil {
		panic(fmt.Sprintf("receipt %s vanished: %v", w.receiptID, err))
	}
	return r
}

// currentLine re-reads one line of the scenario's receipt.
func (w *world) currentLine(lineNo int32) returnreceipt.Line {
	l, err := w.currentReceipt().Line(lineNo)
	if err != nil {
		panic(fmt.Sprintf("receipt %s line %d vanished: %v", w.receiptID, lineNo, err))
	}
	return l
}

// currentDisposition re-reads the line's LATEST decision — the superseding
// override when one exists, never the disposition it replaced.
func (w *world) currentDisposition(lineNo int32) *disposition.Disposition {
	d, err := w.disps.FindByReceiptLine(w.ctx, w.receiptID, lineNo)
	if err != nil {
		panic(fmt.Sprintf("disposition for %s line %d vanished: %v", w.receiptID, lineNo, err))
	}
	return d
}

// finish fails the scenario when a command failed and no step looked at it.
func (w *world) finish() error {
	if w.lastErr != nil && !w.errChecked {
		w.unchecked = append(w.unchecked, w.lastErr)
	}
	if len(w.unchecked) == 0 {
		return nil
	}
	return fmt.Errorf("unchecked command errors: %w", errors.Join(w.unchecked...))
}

// InitializeScenario registers every step.
func InitializeScenario(sc *godog.ScenarioContext) {
	w := &world{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		w.start()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		if err != nil {
			return ctx, err
		}
		return ctx, w.finish()
	})
	registerSetupSteps(sc, w)
	registerReturnSteps(sc, w)
	registerReceiptSteps(sc, w)
	registerDispositionSteps(sc, w)
	registerCommandErrorSteps(sc, w)
}
