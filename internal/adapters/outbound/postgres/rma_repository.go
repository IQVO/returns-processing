package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
)

// openRMAIndex is the partial unique index behind invariant 1 (ADR 0002).
const openRMAIndex = "uq_rmas_one_open_per_order_line"

// RMAs is the pgxpool-backed ports.ReturnAuthorizationRepo.
type RMAs struct {
	pool *pgxpool.Pool
}

// NewRMAs constructs an RMAs repo over pool.
func NewRMAs(pool *pgxpool.Pool) *RMAs { return &RMAs{pool: pool} }

const rmaColumns = `rma_id, order_id, line_no, sku, quantity, site_id, reason_code, status,
	rejection_code, window_ends_at, decided_at, received_at, closed_at, version`

// Save persists the RMA in ONE version-guarded upsert: a fresh id inserts;
// an existing id updates only when the stored version is exactly the version
// the caller loaded (the aggregate's new version minus one — every legal
// save persists exactly one transition, the shape the version-counter
// guarantees). Zero rows affected or a primary-key race is
// ports.ErrConcurrentModification; the one-open partial index maps to the
// domain's ErrOpenRMAExists so the race the use case cannot see still
// answers with the domain error.
func (r *RMAs) Save(ctx context.Context, rma *returnauthorization.RMA) error {
	s := rma.Snapshot()
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO return_authorizations (`+rmaColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (rma_id) DO UPDATE
		SET order_id = EXCLUDED.order_id, line_no = EXCLUDED.line_no, sku = EXCLUDED.sku,
		    quantity = EXCLUDED.quantity, site_id = EXCLUDED.site_id,
		    reason_code = EXCLUDED.reason_code, status = EXCLUDED.status,
		    rejection_code = EXCLUDED.rejection_code, window_ends_at = EXCLUDED.window_ends_at,
		    decided_at = EXCLUDED.decided_at, received_at = EXCLUDED.received_at,
		    closed_at = EXCLUDED.closed_at, version = EXCLUDED.version, updated_at = now()
		WHERE return_authorizations.version = $15
	`, rmaArgs(s)...)
	if err != nil {
		return rmaSaveError(err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrConcurrentModification
	}
	return nil
}

// rmaSaveError maps the two unique violations an RMA write can hit: the
// primary key means another writer created the id first, the partial index
// means another open RMA already holds the order line.
func rmaSaveError(err error) error {
	if isUniqueViolation(err, "return_authorizations_pkey") {
		return ports.ErrConcurrentModification
	}
	if isUniqueViolation(err, openRMAIndex) {
		return returnauthorization.ErrOpenRMAExists
	}
	return fmt.Errorf("save rma: %w", err)
}

// rmaArgs builds the upsert parameters: the fourteen column values plus the
// loaded-version guard (new version minus one).
func rmaArgs(s returnauthorization.Snapshot) []any {
	return []any{
		s.ID, s.OrderID, s.LineNo, s.SKU, s.Quantity, s.SiteID, s.ReasonCode,
		string(s.Status), nullString(string(s.RejectionCode)),
		nullTime(s.WindowEndsAt), nullTime(s.DecidedAt),
		nullTime(s.ReceivedAt), nullTime(s.ClosedAt),
		s.Version, s.Version - 1,
	}
}

// nullTime maps the zero time to SQL NULL; nullString maps "" to SQL NULL.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// timeOrNull maps SQL NULL back to the zero time.
func timeOrNull(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func stringOrNull(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// FindByID implements ports.ReturnAuthorizationRepo.
func (r *RMAs) FindByID(ctx context.Context, id string) (*returnauthorization.RMA, error) {
	row := queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+rmaColumns+` FROM return_authorizations WHERE rma_id = $1`, id)
	rma, err := scanRMA(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	return rma, err
}

// FindOpenByOrderLine implements ports.ReturnAuthorizationRepo: the partial
// unique index guarantees at most one row.
func (r *RMAs) FindOpenByOrderLine(ctx context.Context, orderID string, lineNo int32) (*returnauthorization.RMA, error) {
	row := queryFor(ctx, r.pool).QueryRow(ctx, `
		SELECT `+rmaColumns+` FROM return_authorizations
		WHERE order_id = $1 AND line_no = $2
		  AND status IN ('REQUESTED', 'AUTHORIZED', 'RECEIVED')
		ORDER BY rma_id
		LIMIT 1
	`, orderID, lineNo)
	rma, err := scanRMA(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	return rma, err
}

// ListByOrder implements ports.ReturnAuthorizationRepo: by line number then
// id, byte order (the "C" collation) — the same order the memory double
// returns.
func (r *RMAs) ListByOrder(ctx context.Context, orderID string) ([]*returnauthorization.RMA, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, `
		SELECT `+rmaColumns+` FROM return_authorizations
		WHERE order_id = $1
		ORDER BY line_no, rma_id
	`, orderID)
	if err != nil {
		return nil, fmt.Errorf("list rmas of order %s: %w", orderID, err)
	}
	defer rows.Close()
	out := []*returnauthorization.RMA{}
	for rows.Next() {
		rma, err := scanRMA(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rma)
	}
	return out, rows.Err()
}

// scanRMA hydrates one row through the domain's Snapshot/Restore pair; a
// corrupt row surfaces as an error, never as a live aggregate.
func scanRMA(row pgx.Row) (*returnauthorization.RMA, error) {
	var (
		s                   returnauthorization.Snapshot
		status              string
		rejection           *string
		windowEnds, decided *time.Time
		received, closed    *time.Time
	)
	if err := row.Scan(&s.ID, &s.OrderID, &s.LineNo, &s.SKU, &s.Quantity, &s.SiteID, &s.ReasonCode,
		&status, &rejection, &windowEnds, &decided, &received, &closed, &s.Version); err != nil {
		return nil, err
	}
	s.Status = returnauthorization.Status(status)
	s.RejectionCode = returnauthorization.RejectionCode(stringOrNull(rejection))
	s.WindowEndsAt = timeOrNull(windowEnds)
	s.DecidedAt = timeOrNull(decided)
	s.ReceivedAt = timeOrNull(received)
	s.ClosedAt = timeOrNull(closed)
	rma, err := returnauthorization.Restore(s)
	if err != nil {
		return nil, fmt.Errorf("stored rma %s is invalid: %w", s.ID, err)
	}
	return rma, nil
}
