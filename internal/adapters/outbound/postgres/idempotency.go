package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/application/views"
)

// IdempotencyStore is the pgxpool-backed ports.IdempotencyStore: the answers
// of the writes made with an Idempotency-Key (table idempotency_keys,
// migration 0002). A 2xx answer is saved inside the request's unit of work —
// the same transaction as the handler's effects and their outbox rows — so
// the answer and the effects commit or roll back together.
type IdempotencyStore struct {
	pool *pgxpool.Pool
}

// NewIdempotencyStore constructs an IdempotencyStore over pool.
func NewIdempotencyStore(pool *pgxpool.Pool) *IdempotencyStore {
	return &IdempotencyStore{pool: pool}
}

// Lookup implements ports.IdempotencyStore.
func (s *IdempotencyStore) Lookup(ctx context.Context, key string) (views.IdempotencyRecord, bool, error) {
	rec := views.IdempotencyRecord{Key: key}
	err := queryFor(ctx, s.pool).QueryRow(ctx, `
		SELECT fingerprint, status, content_type, body, created_at
		FROM idempotency_keys WHERE key = $1
	`, key).Scan(&rec.Fingerprint, &rec.Status, &rec.ContentType, &rec.Body, &rec.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return views.IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return views.IdempotencyRecord{}, false, fmt.Errorf("lookup idempotency key: %w", err)
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	return rec, true, nil
}

// Save implements ports.IdempotencyStore: the first writer of a key wins and
// a later Save of the same key changes nothing. Under concurrency the second
// Save blocks on the key's primary key until the first transaction ends, so
// a Save-then-Lookup inside one transaction sees the committed winner.
func (s *IdempotencyStore) Save(ctx context.Context, rec views.IdempotencyRecord) error {
	_, err := queryFor(ctx, s.pool).Exec(ctx, `
		INSERT INTO idempotency_keys (key, fingerprint, status, content_type, body, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (key) DO NOTHING
	`, rec.Key, rec.Fingerprint, rec.Status, rec.ContentType, nonNilBytes(rec.Body), rec.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("save idempotency key: %w", err)
	}
	return nil
}

// nonNilBytes keeps a nil body from becoming SQL NULL (the column is NOT NULL).
func nonNilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}
