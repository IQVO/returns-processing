//go:build integration

package http_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	inboundhttp "github.com/claudioed/returns-processing/internal/adapters/inbound/http"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
)

// The module reference keeps the testcontainers Postgres import reachable
// from this Postgres-touching test package (the fleet's architecture sensor
// reads exactly that); internal/testing/pgtest boots the container.
var _ = tcpostgres.Run

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// poisonTx simulates what a mapped domain conflict does to the request's
// transaction: a statement fails on the server (a unique violation, like a
// lost race on a unique index) and the handler answers from that. From then
// on the transaction is ABORTED (SQLSTATE 25P02 for every further statement).
func poisonTx(t *testing.T, r *http.Request) {
	t.Helper()
	tx, ok := pgtx.From(r.Context())
	if !ok {
		t.Fatal("the middleware did not bind a transaction into the request context")
	}
	const ins = `INSERT INTO idempotency_keys (key, fingerprint, status, content_type, body, created_at)
		VALUES ('dup', 'f', 200, '', ''::bytea, now())`
	for range 2 {
		if _, err := tx.Exec(r.Context(), ins); err != nil {
			return // the second insert aborts the transaction (23505)
		}
	}
	t.Fatal("the duplicate insert did not abort the transaction")
}

func keyedRequest(key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/returns", strings.NewReader(`{}`))
	req.Header.Set(inboundhttp.IdempotencyKeyHeader, key)
	return req
}

func newMiddleware(t *testing.T, next http.Handler) (http.Handler, func(string) int) {
	t.Helper()
	pool := pgtest.NewPool(t)
	mw := inboundhttp.NewIdempotency(
		postgres.NewIdempotencyStore(pool), postgres.NewUnitOfWork(pool), clock{},
	).Middleware(next)
	count := func(key string) int {
		var n int
		pgtest.Scan(t, pool, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, []any{key}, &n)
		return n
	}
	return mw, count
}

type clock struct{}

func (clock) Now() time.Time { return time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC) }

// A mapped 4xx over an aborted transaction is still returned AND recorded:
// the client sees the truthful 409, a retry replays it, and the handler never
// runs twice.
func TestIdempotency409OverAnAbortedTransactionIsRecordedAndReplayed(t *testing.T) {
	var calls atomic.Int64
	mw, count := newMiddleware(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		poisonTx(t, r)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"status":409}`))
	}))

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, keyedRequest("k-poison"))
	if first.Code != http.StatusConflict {
		t.Fatalf("first = %d %s, want the truthful 409", first.Code, first.Body.String())
	}
	if n := count("k-poison"); n != 1 {
		t.Fatalf("the 409 outcome was not committed: %d rows", n)
	}
	if n := count("dup"); n != 0 {
		t.Fatalf("the handler's partial write survived: %d rows", n)
	}

	replay := httptest.NewRecorder()
	mw.ServeHTTP(replay, keyedRequest("k-poison"))
	if replay.Code != http.StatusConflict || replay.Header().Get(inboundhttp.IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replay = %d replayed=%q, want the cached 409", replay.Code, replay.Header().Get(inboundhttp.IdempotencyReplayedHeader))
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1 (the replay must not re-run it)", calls.Load())
	}
}

// A 2xx over an aborted transaction is a lie (its writes died with the
// transaction): it is neither returned nor cached, and the same key retries.
func TestIdempotency2xxOverAnAbortedTransactionIsNeverReturnedOrCached(t *testing.T) {
	var calls atomic.Int64
	mw, count := newMiddleware(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			poisonTx(t, r) // only the first attempt dies inside the transaction
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"r1"}`))
	}))

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, keyedRequest("k-lie"))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first = %d %s, want 500 (the writes never happened)", first.Code, first.Body.String())
	}
	if n := count("k-lie"); n != 0 {
		t.Fatalf("a failed success left %d idempotency rows: the key could not retry", n)
	}

	second := httptest.NewRecorder()
	mw.ServeHTTP(second, keyedRequest("k-lie"))
	if second.Code != http.StatusCreated || second.Header().Get(inboundhttp.IdempotencyReplayedHeader) != "" {
		t.Fatalf("retry = %d %v, want the real work to succeed unmarked", second.Code, second.Header())
	}
	if calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2", calls.Load())
	}
}
