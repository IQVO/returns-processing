package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Every write route requires an Idempotency-Key: 400 idempotency-key-required
// without one, and nothing is stored.
func TestIdempotencyKeyRequiredOnEveryWrite(t *testing.T) {
	f := newFixture(t)
	routes := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/returns", `{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`},
		{http.MethodPost, "/returns/rma-1/receipts", `{"siteId":"WH1","lines":[{"lineNo":1,"sku":"SKU-1","quantity":2,"kind":"UNEXPECTED"}]}`},
		{http.MethodPost, "/receipts/rcpt-1/lines/1/inspections", `{"grade":"A","quantity":1}`},
		{http.MethodPost, "/dispositions/disp-1/overrides", `{"outcome":"RTV","reason":"x"}`},
		{http.MethodPut, "/disposition-policies/2", `{"returnWindowDays":30,"maxUnitsPerLine":10,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"}]}`},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			status, raw, _ := f.call(route.method, route.path, route.body, "")
			wantProblem(t, status, raw, "idempotency-key-required", "")
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d", status)
			}
		})
	}
	if n := f.store.Len(); n != 0 {
		t.Fatalf("store holds %d answers after key-less writes", n)
	}
	// Reads need no key.
	f.mustCall(http.MethodGet, "/returns?orderRef=ord-7c9e", "", "", http.StatusOK)
	f.mustCall(http.MethodGet, "/healthz", "", "", http.StatusOK)
}

// A replay returns the STORED answer byte for byte (status, body, headers),
// and the same key with a different body is 409 idempotency-key-reused.
func TestIdempotencyReplayAndReuse(t *testing.T) {
	f := newFixture(t)
	const body = `{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`
	status1, raw1, header1 := f.call(http.MethodPost, "/returns", body, "k-replay")
	if status1 != http.StatusCreated {
		t.Fatalf("first = %d %s", status1, raw1)
	}
	if got := header1.Get(IdempotencyReplayedHeader); got != "" {
		t.Fatalf("the first execution carries %s: %q", IdempotencyReplayedHeader, got)
	}
	status2, raw2, header2 := f.call(http.MethodPost, "/returns", body, "k-replay")
	if status2 != status1 || raw2 != raw1 || header2.Get("Location") != header1.Get("Location") {
		t.Fatalf("replay differs: %d %s %q vs %d %s %q", status2, raw2, header2.Get("Location"), status1, raw1, header1.Get("Location"))
	}
	if got := header2.Get("Idempotency-Replayed"); got != "true" {
		t.Fatalf("replay Idempotency-Replayed = %q, want true", got)
	}
	if n := f.store.Len(); n != 1 {
		t.Fatalf("store = %d answers, want 1", n)
	}
	status3, raw3, _ := f.call(http.MethodPost, "/returns",
		`{"orderId":"ord-other","lineNo":1,"sku":"SKU-2","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`, "k-replay")
	wantProblem(t, status3, raw3, "idempotency-key-reused", "")
	if status3 != http.StatusConflict {
		t.Fatalf("status = %d", status3)
	}
	// Only 2xx answers are stored on the fresh path; the 4xx reuse is stored
	// too (its own replay), so the store still holds exactly one row.
	if n := f.store.Len(); n != 1 {
		t.Fatalf("store = %d answers after reuse, want 1", n)
	}
}

// A 4xx answer is stored on its own and replays: the same key and body get
// the same rejection without the use case running again.
func TestIdempotencyStoresRejections(t *testing.T) {
	f := newFixture(t)
	const overCap = `{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-1","quantity":99,"siteId":"WH1","reasonCode":"DAMAGED"}`
	status1, raw1, _ := f.call(http.MethodPost, "/returns", overCap, "k-reject")
	wantProblem(t, status1, raw1, "return-not-authorizable", "QUANTITY_EXCEEDS_POLICY")
	status2, raw2, _ := f.call(http.MethodPost, "/returns", overCap, "k-reject")
	if status2 != status1 || raw2 != raw1 {
		t.Fatalf("rejection replay differs: %d %s vs %d %s", status2, raw2, status1, raw1)
	}
	// Nothing was authorized by the refused request.
	if _, raw, _ := f.call(http.MethodGet, "/returns?orderRef=ord-7c9e", "", ""); strings.Contains(raw, "rmaId") {
		t.Fatalf("a rejected authorize left an RMA behind: %s", raw)
	}
}

// A NUL byte in the path answers 404 and in the body 400
// malformed-request-body — both BEFORE idempotency records anything.
func TestRejectNUL(t *testing.T) {
	f := newFixture(t)
	// chi routes never contain a NUL, so the guard is what answers.
	status, raw, _ := f.call(http.MethodGet, "/returns/rma%00-1", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("NUL path = %d %s, want 404", status, raw)
	}
	wantProblem(t, status, raw, "not-found", "NUL")

	const body = "{\"orderId\":\"ord-7c9e\x00\",\"lineNo\":1,\"sku\":\"SKU-1\",\"quantity\":2,\"siteId\":\"WH1\",\"reasonCode\":\"DAMAGED\"}"
	status, raw, _ = f.call(http.MethodPost, "/returns", body, "k-nul")
	wantProblem(t, status, raw, "malformed-request-body", "NUL")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d", status)
	}
	if n := f.store.Len(); n != 0 {
		t.Fatalf("the NUL body was recorded: %d answers", n)
	}
	// The corrected retry with the SAME key runs for real.
	f.mustCall(http.MethodPost, "/returns",
		`{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`, "k-nul", http.StatusCreated)
}

// A key longer than 128 characters, or with a character outside printable
// ASCII, is 400 idempotency-key-invalid (a missing key is
// idempotency-key-required).
func TestIdempotencyKeyInvalid(t *testing.T) {
	f := newFixture(t)
	for name, key := range map[string]string{
		"too long":  strings.Repeat("x", 129),
		"non-ASCII": "k-é",
	} {
		t.Run(name, func(t *testing.T) {
			status, raw, _ := f.call(http.MethodPost, "/returns", "{}", key)
			wantProblem(t, status, raw, "idempotency-key-invalid", "1 to 128")
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d", status)
			}
		})
	}
	// 128 characters is the longest valid key: it reaches the handler.
	status, raw, _ := f.call(http.MethodPost, "/returns", "{}", strings.Repeat("x", 128))
	if strings.Contains(raw, "idempotency-key") {
		t.Fatalf("128-char key = %d %s, want it accepted", status, raw)
	}
}

func TestValidKey(t *testing.T) {
	for key, want := range map[string]bool{
		"":                                     false,
		"a":                                    true,
		"550e8400-e29b-41d4-a716-446655440000": true,
		"two words":                            true,
		"tab\there":                            false,
		"nul\x00":                              false,
		"del\x7f":                              false,
		"é":                                    false,
		strings.Repeat("k", 128):               true,
		strings.Repeat("k", 129):               false,
	} {
		if got := validKey(key); got != want {
			t.Errorf("validKey(%q) = %v, want %v", key, got, want)
		}
	}
}

// The key is global: the same key on another route (or method) is a different
// request, so it is 409 idempotency-key-reused, not a fresh key.
func TestIdempotencyKeyIsGlobalAcrossRoutes(t *testing.T) {
	f := newFixture(t)
	const body = `{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`
	f.mustCall(http.MethodPost, "/returns", body, "k-global", http.StatusCreated)
	status, raw, _ := f.call(http.MethodPost, "/returns/rma-1/receipts", body, "k-global")
	wantProblem(t, status, raw, "idempotency-key-reused", "")
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
}

// /healthz is liveness only; /readyz reflects the drain gate and the
// dependency checks.
func TestHealthAndReadiness(t *testing.T) {
	f := newFixture(t)
	f.mustCall(http.MethodGet, "/healthz", "", "", http.StatusOK)
	raw := f.mustCall(http.MethodGet, "/readyz", "", "", http.StatusOK)
	if !strings.Contains(raw, "ready") {
		t.Fatalf("readyz = %s", raw)
	}

	// The drain gate flips readyz to 503 not_ready, healthz stays 200.
	gate := &Readiness{}
	gate.SetNotReady()
	srv := newServerWithReadiness(t, gate)
	status, raw := get(t, srv, "/readyz")
	if status != http.StatusServiceUnavailable || !strings.Contains(raw, "not_ready") {
		t.Fatalf("draining readyz = %d %s", status, raw)
	}
	if status, raw = get(t, srv, "/healthz"); status != http.StatusOK {
		t.Fatalf("healthz while draining = %d %s", status, raw)
	}

	// A failing dependency is 503 dependency-unavailable naming it; the
	// drain gate stays ready and healthz stays 200.
	boom := NewReadiness(time.Millisecond, 10*time.Millisecond, Dependency{Name: "kafka", Check: func(context.Context) error {
		return errors.New("kafka metadata: connection refused")
	}})
	srv = newServerWithReadiness(t, boom)
	status, raw = get(t, srv, "/readyz")
	if status != http.StatusServiceUnavailable || !strings.Contains(raw, "not ready: kafka") {
		t.Fatalf("failing readyz = %d %s", status, raw)
	}
	if !strings.Contains(raw, "dependency-unavailable") {
		t.Fatalf("readyz = %s", raw)
	}
	if status, _ = get(t, srv, "/healthz"); status != http.StatusOK {
		t.Fatalf("healthz with kafka down = %d", status)
	}
}

// The concurrent-same-key proof (one NEW key, N racers, one effect, every
// caller the winner's answer) needs the REAL transactional semantics of the
// Postgres unit of work: the answer Save shares the winner's transaction, so
// a loser's 4xx can never be stored first. The memory UoW double is a
// passthrough and cannot model that ordering, so the test lives with real
// Postgres: cmd/api's integration suite
// (TestRealEntryPointRunsOnRealPostgresAndKafka / the concurrent subtest).

// get performs one GET against a raw handler (no fixture server).
func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// newServerWithReadiness builds a bare router with the given readiness gate.
func newServerWithReadiness(t *testing.T, gate *Readiness) http.Handler {
	t.Helper()
	return NewRouter(&Server{Readiness: gate})
}
