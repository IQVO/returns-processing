package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
)

// A 5xx is rolled back with the key and NEVER stored: the same key retries the
// real work, the retry is a first execution (no Idempotency-Replayed) and its
// 2xx is then stored and replayed.
func TestIdempotencyNeverStoresA5xx(t *testing.T) {
	store := memory.NewIdempotencyStore()
	calls := 0
	h := NewIdempotency(store, memory.NewUnitOfWork(), memory.Clock{T: T0}).Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls == 1 {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
		}))
	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/returns", strings.NewReader(`{}`))
		req.Header.Set(IdempotencyKeyHeader, "k-5xx")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(); rec.Code != http.StatusInternalServerError || rec.Header().Get(IdempotencyReplayedHeader) != "" {
		t.Fatalf("first = %d %v, want an unmarked 500", rec.Code, rec.Header())
	}
	if n := store.Len(); n != 0 {
		t.Fatalf("store holds %d answers after a 5xx, want none", n)
	}
	if rec := do(); rec.Code != http.StatusCreated || rec.Header().Get(IdempotencyReplayedHeader) != "" || calls != 2 {
		t.Fatalf("retry = %d %v after %d calls, want the work to run again (unmarked 201)", rec.Code, rec.Header(), calls)
	}
	if rec := do(); rec.Code != http.StatusCreated || rec.Header().Get(IdempotencyReplayedHeader) != "true" || calls != 2 {
		t.Fatalf("replay = %d %v after %d calls, want the stored 201 marked as a replay", rec.Code, rec.Header(), calls)
	}
}
