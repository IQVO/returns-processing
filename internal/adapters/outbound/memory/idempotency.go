package memory

import (
	"context"
	"sync"

	"github.com/claudioed/returns-processing/internal/application/views"
)

// IdempotencyStore is the in-memory ports.IdempotencyStore double: a map the
// HTTP middleware tests replay against, with the same first-writer-wins rule
// as the Postgres adapter's ON CONFLICT DO NOTHING.
type IdempotencyStore struct {
	mu   sync.Mutex
	rows map[string]views.IdempotencyRecord
}

// NewIdempotencyStore returns an empty double.
func NewIdempotencyStore() *IdempotencyStore {
	return &IdempotencyStore{rows: map[string]views.IdempotencyRecord{}}
}

// Lookup returns the stored answer of key, or ok=false.
func (s *IdempotencyStore) Lookup(_ context.Context, key string) (views.IdempotencyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.rows[key]
	return rec, ok, nil
}

// Save stores rec; the first writer of a key wins.
func (s *IdempotencyStore) Save(_ context.Context, rec views.IdempotencyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rows[rec.Key]; !exists {
		s.rows[rec.Key] = rec
	}
	return nil
}

// Len returns the number of stored answers (assertions).
func (s *IdempotencyStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

// UnitOfWork is the ports.UnitOfWork double: fn runs unwrapped, so the HTTP
// middleware's request-wide unit is a no-op seam in tests (the Postgres
// adapter's Do is the real transaction).
type UnitOfWork struct{}

// NewUnitOfWork returns the passthrough double.
func NewUnitOfWork() *UnitOfWork { return &UnitOfWork{} }

// Do runs fn as-is.
func (UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
