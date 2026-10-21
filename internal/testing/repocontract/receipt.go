package repocontract

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// ReturnReceiptRepo runs the ReturnReceiptRepo contract. newRepo must return
// an EMPTY repository on every call.
func ReturnReceiptRepo(t *testing.T, newRepo func(t *testing.T) ports.ReturnReceiptRepo) {
	t.Helper()
	run(t, newRepo, map[string]func(t *testing.T, r ports.ReturnReceiptRepo){
		"an unknown receipt is not found":               receiptNotFound,
		"save then find round trips an opened receipt":  receiptRoundTrip,
		"an inspected line round trips its facts":       receiptInspectedRoundTrip,
		"the version guard refuses re-saves and ghosts": receiptVersionGuard,
		"a read-back receipt has no pending events":     receiptNoPendingEvents,
	})
}

func receiptNotFound(t *testing.T, r ports.ReturnReceiptRepo) {
	_, err := r.FindByID(bg, "rc-404")
	wantErr(t, err, ports.ErrNotFound)
}

func receiptRoundTrip(t *testing.T, r ports.ReturnReceiptRepo) {
	in := Opened(t, "rc-1")
	must(t, r.Save(bg, in))
	got, err := r.FindByID(bg, "rc-1")
	must(t, err)
	if got.Version() != 1 || got.RMAID() != "rma-1" || got.SiteID() != "SITE1" {
		t.Fatalf("root facts lost: %+v", got.Snapshot())
	}
	if !reflect.DeepEqual(got.Snapshot(), in.Snapshot()) {
		t.Fatalf("round trip changed the state:\ngot  %+v\nwant %+v", got.Snapshot(), in.Snapshot())
	}
}

func receiptInspectedRoundTrip(t *testing.T, r ports.ReturnReceiptRepo) {
	in := Opened(t, "rc-1")
	must(t, r.Save(bg, in))
	must(t, in.Inspect(1, shared.GradeA, 2, []string{"torn-box"}, T0.Add(time.Hour)))
	must(t, r.Save(bg, in))
	got, err := r.FindByID(bg, "rc-1")
	must(t, err)
	if got.Version() != 2 {
		t.Fatalf("version = %d", got.Version())
	}
	line, err := got.Line(1)
	must(t, err)
	if !line.Inspected() || line.Grade() != shared.GradeA || line.InspectedQuantity() != 2 {
		t.Fatalf("inspection lost: %+v", line)
	}
	if reasons := line.ReasonCodes(); len(reasons) != 1 || reasons[0] != "torn-box" {
		t.Fatalf("reasons = %v", reasons)
	}
	other, err := got.Line(2)
	must(t, err)
	if other.Inspected() || other.Kind() != returnreceipt.KindUnexpected {
		t.Fatalf("line 2 changed: %+v", other)
	}
	// the restored line refuses a second inspection (invariant 3)
	if err := got.Inspect(1, shared.GradeB, 1, nil, T0); !errors.Is(err, returnreceipt.ErrLineAlreadyInspected) {
		t.Fatalf("re-inspect = %v", err)
	}
}

func receiptVersionGuard(t *testing.T, r ports.ReturnReceiptRepo) {
	first := Opened(t, "rc-1")
	must(t, r.Save(bg, first))
	// re-saving the same version is a concurrent modification
	wantErr(t, r.Save(bg, first), ports.ErrConcurrentModification)
	// a fresh aggregate over an existing id is refused too
	wantErr(t, r.Save(bg, Opened(t, "rc-1")), ports.ErrConcurrentModification)
	// the legal +1 transition lands
	must(t, first.Inspect(2, shared.GradeB, 1, nil, T0.Add(time.Hour)))
	must(t, r.Save(bg, first))
	// a second writer's +1 is refused
	second := Opened(t, "rc-1")
	must(t, second.Inspect(2, shared.GradeC, 1, nil, T0.Add(30*time.Minute)))
	wantErr(t, r.Save(bg, second), ports.ErrConcurrentModification)
	got, err := r.FindByID(bg, "rc-1")
	must(t, err)
	if got.Version() != 2 {
		t.Fatalf("stored version = %d", got.Version())
	}
}

func receiptNoPendingEvents(t *testing.T, r ports.ReturnReceiptRepo) {
	must(t, r.Save(bg, Opened(t, "rc-1")))
	got, err := r.FindByID(bg, "rc-1")
	must(t, err)
	if events := got.DomainEvents(); len(events) != 0 {
		t.Fatalf("events = %v", events)
	}
}
