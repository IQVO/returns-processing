// Package repocontract holds the behavioural contract of the outbound
// repositories and read models. The in-memory adapters and the Postgres
// adapters run the SAME cases, so what BDD and the unit tests show is what
// production does.
package repocontract

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// T0 is the fixed instant the cases build on. It has no sub-microsecond
// part, so a Postgres round trip returns it unchanged.
var T0 = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

var bg = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

// Authorized builds an AUTHORIZED RMA at version 1, the way the use case
// does, for orderID's lineNo carrying qty units of the sku.
func Authorized(t *testing.T, id, orderID string, lineNo int32, qty int64) *returnauthorization.RMA {
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
		WindowEndsAt:       T0.Add(30 * 24 * time.Hour),
		Now:                T0,
	}); err != nil {
		t.Fatal(err)
	}
	return rma
}

// Rejected builds a REJECTED RMA at version 1 (a terminal decision frees the
// order line).
func Rejected(t *testing.T, id, orderID string, lineNo int32) *returnauthorization.RMA {
	t.Helper()
	rma, err := returnauthorization.Request(returnauthorization.RequestInput{
		RMAID: id, OrderID: orderID, LineNo: lineNo, SKU: "sku-1",
		Quantity: 1, SiteID: "SITE1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rma.Reject(returnauthorization.RejectReturnWindowExpired, T0); err != nil {
		t.Fatal(err)
	}
	return rma
}

// Opened builds an opened receipt at version 1 with one MATCHED line
// (sku-1 x 3 against rma-1) and one UNEXPECTED line (sku-2 x 1).
func Opened(t *testing.T, id string) *returnreceipt.Receipt {
	t.Helper()
	m, err := returnreceipt.MatchedLine(1, "sku-1", 3, "rma-1")
	if err != nil {
		t.Fatal(err)
	}
	u, err := returnreceipt.UnexpectedLine(2, "sku-2", 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := returnreceipt.Open(returnreceipt.OpenInput{
		ReceiptID: id, RMAID: "rma-1", SiteID: "SITE1",
		Lines: []returnreceipt.Line{m, u}, At: T0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Declared builds the standard total policy: A/B restock, C liquidates,
// SCRAP disposes, veto DISPOSE.
func Declared(t *testing.T, version int64) *disposition.DispositionPolicy {
	t.Helper()
	p, err := disposition.Declare(disposition.PolicyInput{
		Version: version, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
		VetoOutcome: disposition.OutcomeDispose,
		Rules: []disposition.Rule{
			{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
			{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
		DeclaredAt: T0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Decided builds one policy decision (grade A -> RESTOCK, staged) for
// receiptID's lineNo, the way the inspect use case does.
func Decided(t *testing.T, id, receiptID string, lineNo int32) *disposition.Disposition {
	t.Helper()
	d, err := disposition.DecideForLine(disposition.LineDecision{
		DispositionID: id, ReceiptID: receiptID, LineNo: lineNo,
		SKU: "sku-1", Quantity: 3, Grade: shared.GradeA,
		Classification: disposition.Classification{SKU: "sku-1", ProductClass: "*", Classified: true},
		Policy:         Declared(t, 1), SiteID: "SITE1", RMAID: "rma-1",
		ReceivedAt: T0, DecidedAt: T0.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// run executes the named cases, each on a fresh repo from newRepo.
func run[T any](t *testing.T, newRepo func(t *testing.T) T, cases map[string]func(t *testing.T, r T)) {
	t.Helper()
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newRepo(t)) })
	}
}

// --- RMA repo ---------------------------------------------------------------

// ReturnAuthorizationRepo runs the ReturnAuthorizationRepo contract. newRepo
// must return an EMPTY repository on every call.
func ReturnAuthorizationRepo(t *testing.T, newRepo func(t *testing.T) ports.ReturnAuthorizationRepo) {
	t.Helper()
	run(t, newRepo, map[string]func(t *testing.T, r ports.ReturnAuthorizationRepo){
		"an unknown rma is not found":                    rmaNotFound,
		"save then find round trips an authorized rma":   rmaRoundTrip,
		"a terminal rma keeps its decision across saves": rmaRejectedRoundTrip,
		"the open lookup finds the open rma only":        rmaOpenLookup,
		"list by order is by line then id":               rmaListByOrder,
		"the version guard refuses stale and re-saves":   rmaVersionGuard,
		"a second open rma per line is refused":          rmaOneOpen,
		"a terminal rma frees the order line":            rmaTerminalFreesLine,
		"the one-open rule is per order and line":        rmaOneOpenScope,
		"a read-back rma has no pending events":          rmaNoPendingEvents,
	})
}

func rmaNotFound(t *testing.T, r ports.ReturnAuthorizationRepo) {
	_, err := r.FindByID(bg, "rma-404")
	wantErr(t, err, ports.ErrNotFound)
	_, err = r.FindOpenByOrderLine(bg, "ord-1", 2)
	wantErr(t, err, ports.ErrNotFound)
}

func rmaRoundTrip(t *testing.T, r ports.ReturnAuthorizationRepo) {
	in := Authorized(t, "rma-1", "ord-1", 2, 3)
	must(t, r.Save(bg, in))
	got, err := r.FindByID(bg, "rma-1")
	must(t, err)
	if got.Status() != returnauthorization.StatusAuthorized || got.Version() != 1 {
		t.Fatalf("state = %s/%d", got.Status(), got.Version())
	}
	if !reflect.DeepEqual(got.Snapshot(), in.Snapshot()) {
		t.Fatalf("round trip changed the state:\ngot  %+v\nwant %+v", got.Snapshot(), in.Snapshot())
	}
	if !got.WindowEndsAt().Equal(in.WindowEndsAt()) {
		t.Fatalf("window = %v, want %v", got.WindowEndsAt(), in.WindowEndsAt())
	}
}

func rmaRejectedRoundTrip(t *testing.T, r ports.ReturnAuthorizationRepo) {
	in := Rejected(t, "rma-1", "ord-1", 2)
	must(t, r.Save(bg, in))
	got, err := r.FindByID(bg, "rma-1")
	must(t, err)
	if got.Status() != returnauthorization.StatusRejected ||
		got.RejectionCode() != returnauthorization.RejectReturnWindowExpired {
		t.Fatalf("state = %s/%s", got.Status(), got.RejectionCode())
	}
	if !reflect.DeepEqual(got.Snapshot(), in.Snapshot()) {
		t.Fatalf("round trip changed the state:\ngot  %+v\nwant %+v", got.Snapshot(), in.Snapshot())
	}
}

func rmaOpenLookup(t *testing.T, r ports.ReturnAuthorizationRepo) {
	must(t, r.Save(bg, Authorized(t, "rma-1", "ord-1", 2, 3)))
	must(t, r.Save(bg, Rejected(t, "rma-2", "ord-1", 4)))
	got, err := r.FindOpenByOrderLine(bg, "ord-1", 2)
	must(t, err)
	if got.ID() != "rma-1" {
		t.Fatalf("open lookup = %s", got.ID())
	}
	if _, err := r.FindOpenByOrderLine(bg, "ord-1", 4); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("a rejected rma is not open: %v", err)
	}
}

func rmaListByOrder(t *testing.T, r ports.ReturnAuthorizationRepo) {
	must(t, r.Save(bg, Authorized(t, "rma-b", "ord-1", 5, 1)))
	must(t, r.Save(bg, Authorized(t, "rma-a", "ord-1", 2, 1)))
	must(t, r.Save(bg, Authorized(t, "rma-c", "ord-2", 1, 1)))
	list, err := r.ListByOrder(bg, "ord-1")
	must(t, err)
	if len(list) != 2 || list[0].ID() != "rma-a" || list[1].ID() != "rma-b" {
		t.Fatalf("list = %v,%v", list[0].ID(), list[1].ID())
	}
	empty, err := r.ListByOrder(bg, "ord-9")
	must(t, err)
	if len(empty) != 0 {
		t.Fatalf("empty order listed %d rows", len(empty))
	}
}

func rmaVersionGuard(t *testing.T, r ports.ReturnAuthorizationRepo) {
	first := Authorized(t, "rma-1", "ord-1", 2, 3)
	must(t, r.Save(bg, first))
	// re-saving the same version is a concurrent modification
	wantErr(t, r.Save(bg, first), ports.ErrConcurrentModification)
	// the legal +1 transition lands
	must(t, first.MarkReceived(T0.Add(time.Hour)))
	must(t, r.Save(bg, first))
	// a second writer's +1 (loaded before the transition above) is refused:
	// its guard version no longer matches the stored row
	second := Authorized(t, "rma-1", "ord-1", 2, 3)
	must(t, second.MarkReceived(T0.Add(30*time.Minute)))
	wantErr(t, r.Save(bg, second), ports.ErrConcurrentModification)
	// saving a fresh aggregate over an existing id is refused too
	wantErr(t, r.Save(bg, Authorized(t, "rma-1", "ord-1", 2, 3)), ports.ErrConcurrentModification)
	got, err := r.FindByID(bg, "rma-1")
	must(t, err)
	if got.Version() != 2 || got.Status() != returnauthorization.StatusReceived {
		t.Fatalf("stored = %s/%d", got.Status(), got.Version())
	}
}

func rmaOneOpen(t *testing.T, r ports.ReturnAuthorizationRepo) {
	must(t, r.Save(bg, Authorized(t, "rma-1", "ord-1", 2, 3)))
	wantErr(t, r.Save(bg, Authorized(t, "rma-2", "ord-1", 2, 1)), returnauthorization.ErrOpenRMAExists)
	if _, err := r.FindByID(bg, "rma-2"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("the refused rma must not have been stored")
	}
}

func rmaTerminalFreesLine(t *testing.T, r ports.ReturnAuthorizationRepo) {
	must(t, r.Save(bg, Rejected(t, "rma-1", "ord-1", 2)))
	must(t, r.Save(bg, Authorized(t, "rma-2", "ord-1", 2, 1)))
}

func rmaOneOpenScope(t *testing.T, r ports.ReturnAuthorizationRepo) {
	must(t, r.Save(bg, Authorized(t, "rma-1", "ord-1", 2, 3)))
	must(t, r.Save(bg, Authorized(t, "rma-2", "ord-1", 3, 3))) // other line
	must(t, r.Save(bg, Authorized(t, "rma-3", "ord-2", 2, 3))) // other order
}

func rmaNoPendingEvents(t *testing.T, r ports.ReturnAuthorizationRepo) {
	must(t, r.Save(bg, Authorized(t, "rma-1", "ord-1", 2, 3)))
	got, err := r.FindByID(bg, "rma-1")
	must(t, err)
	if events := got.DomainEvents(); len(events) != 0 {
		t.Fatalf("events = %v", events)
	}
}
