package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// T0 is the fixture clock: every decision the handlers drive is stamped with
// it, so the wire instants are exact.
var T0 = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

// fixture is the whole service over the memory doubles: the use cases wired
// the way cmd/ wires them over Postgres, behind the real router with the
// idempotency middleware over the memory store.
type fixture struct {
	t        *testing.T
	srv      *httptest.Server
	store    *memory.IdempotencyStore
	rmas     *memory.RMAs
	events   *memory.Events
	policy   *memory.Policies
	receipts *memory.Receipts
	disp     *memory.Dispositions
}

// newFixture boots the router over seeded doubles: policy v1 (window 30
// days, cap 5, the default grade table), one allocated order line
// (ord-7c9e/1 = SKU-1) and its manifested work unit.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureOpt(t, true)
}

// newFixtureWithoutPolicy boots the same world with NO policy declared (the
// missing-policy refusals).
func newFixtureWithoutPolicy(t *testing.T) *fixture {
	t.Helper()
	return newFixtureOpt(t, false)
}

func newFixtureOpt(t *testing.T, withPolicy bool) *fixture {
	t.Helper()
	var (
		rmas       = memory.NewRMAs()
		receipts   = memory.NewReceipts()
		disp       = memory.NewDispositions()
		policies   = memory.NewPolicies()
		events     = &memory.Events{}
		allocated  = memory.NewAllocatedLines()
		manifested = memory.NewManifestedUnits()
		classes    = memory.NewProductClasses()
		ids        = memory.NewIDGen()
		clock      = memory.Clock{T: T0}
	)
	if withPolicy {
		if _, err := (&usecases.DeclareDispositionPolicy{Policies: policies, Clock: clock}).Execute(t.Context(), policyV1()); err != nil {
			t.Fatal(err)
		}
	}
	allocated.Put("ord-7c9e", 1, views.AllocatedLine{SKU: "SKU-1"})
	manifested.PutByOrderLine("ord-7c9e", 1, views.ManifestedUnit{PackageID: "pkg-1", ManifestedAt: T0})
	// ord-alloc is allocated but never manifested (the LINE_NOT_MANIFESTED
	// refusal); ord-noalloc is in no copy at all (ORDER_LINE_NOT_ALLOCATED).
	allocated.Put("ord-alloc", 1, views.AllocatedLine{SKU: "SKU-2"})

	store := memory.NewIdempotencyStore()
	server := &Server{
		AuthorizeReturn: &usecases.AuthorizeReturn{
			RMAs: rmas, Policies: policies, Allocated: allocated, Manifested: manifested,
			Events: events, IDs: ids, Clock: clock,
		},
		GetReturn:   &usecases.GetReturn{RMAs: rmas},
		ListByOrder: &usecases.ListReturnsByOrder{RMAs: rmas},
		OpenReturnReceipt: &usecases.OpenReturnReceipt{
			Receipts: receipts, Events: events, IDs: ids, Clock: clock,
		},
		InspectReturnLine: &usecases.InspectReturnLine{
			Receipts: receipts, Dispositions: disp, Policies: policies, Products: classes,
			Events: events, IDs: ids, Clock: clock,
		},
		OverrideDisposition: &usecases.OverrideDisposition{
			Dispositions: disp, Events: events, IDs: ids, Clock: clock,
		},
		DeclareDispositionPolicy: &usecases.DeclareDispositionPolicy{Policies: policies, Clock: clock},
		GetDispositionPolicy:     &usecases.GetDispositionPolicy{Policies: policies},
		Idempotency:              NewIdempotency(store, memory.NewUnitOfWork(), clock).Middleware,
		Readiness:                &Readiness{},
	}
	srv := httptest.NewServer(NewRouter(server))
	t.Cleanup(srv.Close)
	return &fixture{t: t, srv: srv, store: store, rmas: rmas, events: events, policy: policies, receipts: receipts, disp: disp}
}

// policyV1 is the fixture's declared table: A/B restock, C liquidate, SCRAP
// dispose (per-grade "*" defaults), window 30 days, cap 5.
func policyV1() usecases.PolicyDeclaration {
	return usecases.PolicyDeclaration{
		Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
		Rules: []disposition.Rule{
			{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
	}
}

// call performs a request against the fixture's server. key is the
// Idempotency-Key ("" sends none).
func (f *fixture) call(method, path, body, key string) (int, string, http.Header) {
	f.t.Helper()
	req, err := http.NewRequestWithContext(f.t.Context(), method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	if key != "" {
		req.Header.Set(IdempotencyKeyHeader, key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

// mustCall asserts the status and returns the body.
func (f *fixture) mustCall(method, path, body, key string, want int) string {
	f.t.Helper()
	status, raw, _ := f.call(method, path, body, key)
	if status != want {
		f.t.Fatalf("%s %s = %d %s, want %d", method, path, status, raw, want)
	}
	return raw
}

// authorize drives the happy path once and returns the new RMA id.
func (f *fixture) authorize(orderID string) string {
	f.t.Helper()
	body := `{"orderId":"` + orderID + `","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`
	raw := f.mustCall(http.MethodPost, "/returns", body, "k-auth-"+orderID, http.StatusCreated)
	return stringField(f.t, raw, "rmaId")
}

// openReceipt opens a one-line MATCHED receipt against rma and returns the
// receipt id.
func (f *fixture) openReceipt(rma string) string {
	f.t.Helper()
	body := `{"siteId":"WH1","lines":[{"lineNo":1,"sku":"SKU-1","quantity":2,"kind":"MATCHED","rmaId":"` + rma + `"}]}`
	raw := f.mustCall(http.MethodPost, "/returns/"+rma+"/receipts", body, "k-receipt-"+rma, http.StatusCreated)
	return stringField(f.t, raw, "receiptId")
}

// stringField digs one string field out of a JSON body (assertions stay
// readable; exact-shape checks have their own tests).
func stringField(t *testing.T, body, field string) string {
	t.Helper()
	m := decodeBody(t, body)
	v, ok := m[field]
	if !ok {
		t.Fatalf("%q not in %s", field, body)
	}
	s, _ := v.(string)
	return s
}

// decodeBody parses a JSON object body.
func decodeBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return m
}

// wantProblem asserts the RFC 7807 shape: status, type slug and (when not
// "") a detail substring.
func wantProblem(t *testing.T, status int, body, slug, detailPart string) {
	t.Helper()
	m := decodeBody(t, body)
	if got, _ := m["type"].(string); got != problemBaseURI+slug {
		t.Fatalf("type = %q, want %q (body %s)", got, problemBaseURI+slug, body)
	}
	if got, _ := m["status"].(float64); int(got) != status {
		t.Fatalf("status = %v, want %d (body %s)", m["status"], status, body)
	}
	if detailPart != "" {
		if d, _ := m["detail"].(string); !strings.Contains(d, detailPart) {
			t.Fatalf("detail %q does not contain %q", d, detailPart)
		}
	}
	if _, ok := m["title"]; !ok {
		t.Fatalf("no title in %s", body)
	}
	if _, ok := m["instance"]; !ok {
		t.Fatalf("no instance in %s", body)
	}
}
