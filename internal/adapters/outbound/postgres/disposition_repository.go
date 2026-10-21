package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// onePolicyDecisionIndex is the partial unique index behind "one policy
// decision per receipt line" (overrides carry supersedes and are exempt).
const onePolicyDecisionIndex = "uq_dispositions_one_policy_decision_per_line"

// Dispositions is the pgxpool-backed ports.DispositionRepo.
type Dispositions struct {
	pool *pgxpool.Pool
}

// NewDispositions constructs a Dispositions repo over pool.
func NewDispositions(pool *pgxpool.Pool) *Dispositions { return &Dispositions{pool: pool} }

const dispositionColumns = `disposition_id, receipt_id, line_no, sku, quantity, grade, outcome,
	policy_version, supersedes, reason, site_id, rma_id, received_at, decided_at, staged, version`

// Save persists the immutable decision in ONE version-guarded upsert (a
// disposition is always at version 1, so an existing id never updates: zero
// rows affected is ports.ErrConcurrentModification, like the memory double).
// The partial unique index maps to the same error: two racing POLICY
// decisions for one line are a concurrent duplicate, the loser sees this.
func (r *Dispositions) Save(ctx context.Context, d *disposition.Disposition) error {
	s := d.Snapshot()
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO dispositions (`+dispositionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (disposition_id) DO UPDATE
		SET staged = EXCLUDED.staged
		WHERE dispositions.version = 0
	`, s.ID, s.ReceiptID, s.LineNo, s.SKU, s.Quantity, string(s.Grade), string(s.Outcome),
		s.PolicyVersion, nullString(s.Supersedes), s.Reason, s.SiteID,
		s.RMAID, s.ReceivedAt.UTC(), s.DecidedAt.UTC(), s.Staged, s.Version)
	if err != nil {
		return dispositionSaveError(err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrConcurrentModification
	}
	return nil
}

// dispositionSaveError maps the unique violations a decision write can hit.
func dispositionSaveError(err error) error {
	if isUniqueViolation(err, "dispositions_pkey") {
		return ports.ErrConcurrentModification
	}
	if isUniqueViolation(err, onePolicyDecisionIndex) {
		return ports.ErrConcurrentModification
	}
	return fmt.Errorf("save disposition: %w", err)
}

// FindByID implements ports.DispositionRepo.
func (r *Dispositions) FindByID(ctx context.Context, id string) (*disposition.Disposition, error) {
	row := queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+dispositionColumns+` FROM dispositions WHERE disposition_id = $1`, id)
	d, err := scanDisposition(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	return d, err
}

// FindByReceiptLine implements ports.DispositionRepo: the line's LATEST
// decision — the superseding override, never the disposition it replaced.
// decided_at DESC with the id as tie-break is the save order under the use
// cases' sequential id generator, the same "saved last" the memory double
// returns.
func (r *Dispositions) FindByReceiptLine(ctx context.Context, receiptID string, lineNo int32) (*disposition.Disposition, error) {
	row := queryFor(ctx, r.pool).QueryRow(ctx, `
		SELECT `+dispositionColumns+` FROM dispositions
		WHERE receipt_id = $1 AND line_no = $2
		ORDER BY decided_at DESC, disposition_id DESC
		LIMIT 1
	`, receiptID, lineNo)
	d, err := scanDisposition(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	return d, err
}

func scanDisposition(row pgx.Row) (*disposition.Disposition, error) {
	var (
		s                  disposition.Snapshot
		grade, outcome     string
		supersedes, reason *string
		rma                *string
	)
	if err := row.Scan(&s.ID, &s.ReceiptID, &s.LineNo, &s.SKU, &s.Quantity, &grade, &outcome,
		&s.PolicyVersion, &supersedes, &reason, &s.SiteID, &rma,
		&s.ReceivedAt, &s.DecidedAt, &s.Staged, &s.Version); err != nil {
		return nil, err
	}
	s.Grade = shared.Grade(grade)
	s.Outcome = disposition.Outcome(outcome)
	s.Supersedes = stringOrNull(supersedes)
	s.Reason = stringOrNull(reason)
	s.RMAID = stringOrNull(rma)
	s.ReceivedAt = s.ReceivedAt.UTC()
	s.DecidedAt = s.DecidedAt.UTC()
	d, err := disposition.Restore(s)
	if err != nil {
		return nil, fmt.Errorf("stored disposition %s is invalid: %w", s.ID, err)
	}
	return d, nil
}
