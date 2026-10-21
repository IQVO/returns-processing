package returnauthorization

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

var (
	now     = time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	window  = now.AddDate(0, 0, 30)
	maxCap  = int64(10)
	workUty = "ord-7c9e2f1a-line-1"
)

func requested(t *testing.T) *RMA {
	t.Helper()
	r, err := Request(RequestInput{
		RMAID: "rma-1", OrderID: "ord-7c9e2f1a", LineNo: 1,
		SKU: "SKU-1", Quantity: 2, SiteID: "WH1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	return r
}

func facts() Facts {
	return Facts{ManifestedWorkUnit: workUty, PolicyMaxUnits: maxCap, WindowEndsAt: window, Now: now}
}

func authorized(t *testing.T) *RMA {
	t.Helper()
	r := requested(t)
	if err := r.Authorize(facts()); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	return r
}

// The happy path: REQUESTED -> AUTHORIZED, one version bump, the window stamped.
func TestAuthorizeTransitionsToAuthorized(t *testing.T) {
	r := requested(t)
	if r.Status() != StatusRequested || !r.Open() || r.Version() != 0 {
		t.Fatalf("fresh rma: status %s open %v version %d", r.Status(), r.Open(), r.Version())
	}
	if err := r.Authorize(facts()); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if r.Status() != StatusAuthorized || r.Version() != 1 || !r.Open() {
		t.Fatalf("authorized: status %s version %d open %v", r.Status(), r.Version(), r.Open())
	}
	if r.WindowEndsAt() != window {
		t.Fatalf("window = %v, want %v", r.WindowEndsAt(), window)
	}
}

// The happy path's event: one ReturnAuthorized carrying the whole decision.
func TestAuthorizeRaisesEvent(t *testing.T) {
	evs := authorized(t).DomainEvents()
	if len(evs) != 1 || evs[0].EventName() != "ReturnAuthorized" {
		t.Fatalf("events = %v, want one ReturnAuthorized", evs)
	}
	auth := evs[0].(ReturnAuthorized)
	if auth.RMAID() != "rma-1" || auth.SKU != "SKU-1" || auth.Quantity != 2 ||
		auth.Site != "WH1" || auth.ReasonCode != "DAMAGED" {
		t.Fatalf("ReturnAuthorized = %+v", auth)
	}
	if auth.OrderRef.WorkUnit() != workUty || auth.WindowEnds != window {
		t.Fatalf("ReturnAuthorized = %+v", auth)
	}
}

// The happy path continues: AUTHORIZED -> RECEIVED -> CLOSED, +1 version per
// transition, no new events (the receipt's own event carries the arrival).
func TestReceiveCloseTransitions(t *testing.T) {
	r := requested(t)
	if err := r.Authorize(facts()); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if err := r.MarkReceived(now.Add(24 * time.Hour)); err != nil {
		t.Fatalf("MarkReceived: %v", err)
	}
	if r.Status() != StatusReceived || !r.Open() {
		t.Fatalf("received: status %s open %v", r.Status(), r.Open())
	}
	if err := r.Close(now.Add(48 * time.Hour)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if r.Status() != StatusClosed || r.Open() {
		t.Fatalf("closed: status %s open %v", r.Status(), r.Open())
	}
	if r.Version() != 3 {
		t.Fatalf("version = %d, want 3", r.Version())
	}
	if evs := r.DomainEvents(); len(evs) != 1 {
		t.Fatalf("events = %d, want still one (the authorization)", len(evs))
	}
}

// The invariant: one open RMA per (order_id, line_no) — a known open sibling
// for the line is refused, whatever its other facts.
func TestOneOpenRMAPerOrderLine(t *testing.T) {
	r := requested(t)
	f := facts()
	f.ExistingOpenRMAID = "rma-earlier"
	if err := r.Authorize(f); !errors.Is(err, ErrOpenRMAExists) {
		t.Fatalf("Authorize with open sibling = %v, want ErrOpenRMAExists", err)
	}
	if r.Status() != StatusRequested || len(r.DomainEvents()) != 0 {
		t.Fatal("a refused authorize must not change the rma")
	}
	if RejectionCodeFor(ErrOpenRMAExists) != RejectOpenRMAExists {
		t.Fatal("code mapping")
	}
}

// The invariant: authorizable only when a manifested work unit exists for
// that line — blank or a different line's work unit both refuse.
func TestAuthorizeRequiresManifestedWorkUnit(t *testing.T) {
	for _, tc := range []struct {
		name string
		wu   string
	}{
		{"blank", ""},
		{"other line", "ord-7c9e2f1a-line-2"},
		{"other order", "ord-other-line-1"},
		{"garbage", "wu-8a1f"},
	} {
		r := requested(t)
		f := facts()
		f.ManifestedWorkUnit = tc.wu
		if err := r.Authorize(f); !errors.Is(err, ErrLineNotManifested) {
			t.Fatalf("%s: Authorize = %v, want ErrLineNotManifested", tc.name, err)
		}
		if RejectionCodeFor(ErrLineNotManifested) != RejectLineNotManifested {
			t.Fatalf("%s: code mapping", tc.name)
		}
	}
}

// The invariant: now must be <= window end. The boundary (now == window end)
// authorizes; one tick later refuses; a missing window or now refuses.
func TestAuthorizeWindow(t *testing.T) {
	r := requested(t)
	f := facts()
	f.Now = window // the boundary is inside
	if err := r.Authorize(f); err != nil {
		t.Fatalf("Authorize at the window boundary = %v", err)
	}
	late := requested(t)
	fl := facts()
	fl.Now = window.Add(1 * time.Nanosecond)
	if err := late.Authorize(fl); !errors.Is(err, ErrWindowExpired) {
		t.Fatalf("Authorize past the window = %v, want ErrWindowExpired", err)
	}
	if RejectionCodeFor(ErrWindowExpired) != RejectReturnWindowExpired {
		t.Fatal("code mapping")
	}
	noWin := requested(t)
	fw := facts()
	fw.WindowEndsAt = time.Time{}
	if err := noWin.Authorize(fw); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("Authorize without a window = %v, want ErrNoWindow", err)
	}
	noNow := requested(t)
	fn := facts()
	fn.Now = time.Time{}
	if err := noNow.Authorize(fn); !errors.Is(err, ErrNoTimestamp) {
		t.Fatalf("Authorize without now = %v, want ErrNoTimestamp", err)
	}
}

// The invariant: quantity in [1, policy.max_units_per_line]. The floor is
// held by Request (a Quantity value object); the ceiling by Authorize, with
// exactly the cap passing.
func TestAuthorizeQuantityBounds(t *testing.T) {
	if _, err := Request(RequestInput{
		RMAID: "rma-q", OrderID: "ord-7c9e2f1a", LineNo: 1,
		SKU: "SKU-1", Quantity: 0, SiteID: "WH1", ReasonCode: "DAMAGED",
	}); !errors.Is(err, shared.ErrQuantityBelowOne) {
		t.Fatalf("quantity 0 = %v, want shared.ErrQuantityBelowOne", err)
	}
	atCap := requested(t)
	fc := facts()
	fc.PolicyMaxUnits = 2 // the rma quantity is exactly the cap
	if err := atCap.Authorize(fc); err != nil {
		t.Fatalf("Authorize at the cap = %v", err)
	}
	over := requested(t)
	fo := facts()
	fo.PolicyMaxUnits = 1
	if err := over.Authorize(fo); !errors.Is(err, ErrQuantityExceedsPolicy) {
		t.Fatalf("Authorize over the cap = %v, want ErrQuantityExceedsPolicy", err)
	}
	if RejectionCodeFor(ErrQuantityExceedsPolicy) != RejectQuantityExceedsMax {
		t.Fatal("code mapping")
	}
	broken := requested(t)
	fb := facts()
	fb.PolicyMaxUnits = 0
	if err := broken.Authorize(fb); !errors.Is(err, ErrInvalidMaxUnits) {
		t.Fatalf("Authorize with cap 0 = %v, want ErrInvalidMaxUnits", err)
	}
}

// The invariant: terminal states have no transitions — nothing leaves
// REJECTED or CLOSED.
func TestTerminalStatesHaveNoTransitions(t *testing.T) {
	rejected := requested(t)
	if err := rejected.Reject(RejectLineNotManifested, now); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.Status() != StatusRejected || rejected.Open() {
		t.Fatalf("rejected: status %s open %v", rejected.Status(), rejected.Open())
	}
	assertNoTransitions(t, rejected)
	if rejected.Version() != 1 {
		t.Fatalf("a refused transition must not bump the version; got %d", rejected.Version())
	}
	assertRejectedEvent(t, rejected)

	closed := requested(t)
	if err := closed.Authorize(facts()); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if err := closed.MarkReceived(now); err != nil {
		t.Fatalf("MarkReceived: %v", err)
	}
	if err := closed.Close(now); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertNoTransitions(t, closed)
}

func assertNoTransitions(t *testing.T, r *RMA) {
	t.Helper()
	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"Authorize", func() error { return r.Authorize(facts()) }},
		{"Reject", func() error { return r.Reject(RejectLineNotManifested, now) }},
		{"MarkReceived", func() error { return r.MarkReceived(now) }},
		{"Close", func() error { return r.Close(now) }},
	} {
		if err := call.fn(); !errors.Is(err, ErrTerminal) {
			t.Fatalf("%s: %s = %v, want ErrTerminal", r.Status(), call.name, err)
		}
	}
}

func assertRejectedEvent(t *testing.T, rejected *RMA) {
	t.Helper()
	evs := rejected.DomainEvents()
	if len(evs) != 1 || evs[0].EventName() != "ReturnAuthorizationRejected" {
		t.Fatalf("events = %v, want one ReturnAuthorizationRejected", evs)
	}
	rej, ok := evs[0].(ReturnAuthorizationRejected)
	if !ok || rej.RejectionCode != RejectLineNotManifested || rej.Quantity != 2 || rej.SKU != "SKU-1" {
		t.Fatalf("ReturnAuthorizationRejected = %+v", rej)
	}
}

// The failing paths of the state machine: only an AUTHORIZED rma receives,
// only a RECEIVED rma closes, a decided rma cannot be decided again, and a
// zero timestamp is refused.
func TestTransitionPreconditions(t *testing.T) {
	fresh := requested(t)
	if err := fresh.MarkReceived(now); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("MarkReceived on REQUESTED = %v", err)
	}
	if err := fresh.Close(now); !errors.Is(err, ErrNotReceived) {
		t.Fatalf("Close on REQUESTED = %v", err)
	}
	authorized := requested(t)
	if err := authorized.Authorize(facts()); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if err := authorized.Close(now); !errors.Is(err, ErrNotReceived) {
		t.Fatalf("Close on AUTHORIZED = %v", err)
	}
	if err := authorized.Authorize(facts()); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second Authorize = %v", err)
	}
	if err := authorized.Reject(RejectOpenRMAExists, now); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("Reject after Authorize = %v", err)
	}
	if err := authorized.MarkReceived(time.Time{}); !errors.Is(err, ErrNoTimestamp) {
		t.Fatalf("MarkReceived zero time = %v", err)
	}
	if err := authorized.MarkReceived(now); err != nil {
		t.Fatalf("MarkReceived: %v", err)
	}
	if err := authorized.Close(time.Time{}); !errors.Is(err, ErrNoTimestamp) {
		t.Fatalf("Close zero time = %v", err)
	}
}

// The failing paths of Request: every identity field is validated, and a
// blank or unknown rejection code never decides an rma.
func TestRequestValidation(t *testing.T) {
	base := RequestInput{
		RMAID: "rma-1", OrderID: "ord-7c9e2f1a", LineNo: 1,
		SKU: "SKU-1", Quantity: 2, SiteID: "WH1", ReasonCode: "DAMAGED",
	}
	for _, tc := range []struct {
		name string
		mut  func(*RequestInput)
		want error
	}{
		{"blank rma id", func(in *RequestInput) { in.RMAID = " " }, shared.ErrBlankID},
		{"nul in rma id", func(in *RequestInput) { in.RMAID = "rma\x00" }, shared.ErrNULInID},
		{"blank order", func(in *RequestInput) { in.OrderID = "" }, shared.ErrBlankID},
		{"blank sku", func(in *RequestInput) { in.SKU = "" }, shared.ErrBlankID},
		{"quantity below one", func(in *RequestInput) { in.Quantity = -1 }, shared.ErrQuantityBelowOne},
		{"blank site", func(in *RequestInput) { in.SiteID = " " }, shared.ErrBlankID},
		{"blank reason", func(in *RequestInput) { in.ReasonCode = " " }, ErrBlankReasonCode},
		{"line below one", func(in *RequestInput) { in.LineNo = 0 }, shared.ErrLineNoBelowOne},
	} {
		in := base
		tc.mut(&in)
		if _, err := Request(in); !errors.Is(err, tc.want) {
			t.Fatalf("%s: Request = %v, want %v", tc.name, err, tc.want)
		}
	}
	r := requested(t)
	if err := r.Reject(RejectionCode("NOPE"), now); !errors.Is(err, ErrInvalidRejectionCode) {
		t.Fatalf("unknown code = %v", err)
	}
	if err := r.Reject(RejectLineNotManifested, time.Time{}); !errors.Is(err, ErrNoTimestamp) {
		t.Fatalf("zero time = %v", err)
	}
	if RejectionCodeFor(ErrNoTimestamp) != "" || RejectionCodeFor(nil) != "" {
		t.Fatal("non-rejection errors map to no code")
	}
	if !StatusRequested.Valid() || !StatusClosed.Valid() || Status("x").Valid() {
		t.Fatal("status validity")
	}
	if !RejectOpenRMAExists.Valid() || RejectionCode("x").Valid() {
		t.Fatal("rejection code validity")
	}
}
