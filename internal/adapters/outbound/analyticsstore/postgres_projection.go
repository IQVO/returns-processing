package analyticsstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/analytics/report"
)

// Projection is the Postgres WRITER (report.Projection). Apply claims the
// event id and folds the event into the fact tables in ONE transaction: the
// id is recorded if and only if its effect is, so a failure anywhere leaves
// nothing behind and the redelivered event is applied afresh, while a replay
// of an applied id is a no-op.
type Projection struct {
	pool *pgxpool.Pool
}

// NewProjection constructs a Projection over the writer pool.
func NewProjection(pool *pgxpool.Pool) *Projection { return &Projection{pool: pool} }

var _ report.Projection = (*Projection)(nil)

const claimSQL = `
	INSERT INTO analytics_processed_events (event_id, event_type, occurred_at)
	VALUES ($1, $2, $3)
	ON CONFLICT (event_id) DO NOTHING`

// Every line upsert keeps the EARLIEST candidate of each milestone: LEAST
// ignores NULLs, so a NULL parameter (the event does not mark that milestone)
// keeps the stored value, and the row converges whatever order the events
// arrive in. The descriptive columns keep the first non-NULL value.
const upsertLineSQL = `
	INSERT INTO return_line_facts (receipt_id, line_no, site_id, sku, quantity, rma_id, disposition_id,
	                               opened_at, inspected_at, decided_at, staged_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8::timestamptz, $9::timestamptz, $10::timestamptz, $11::timestamptz)
	ON CONFLICT (receipt_id, line_no) DO UPDATE SET
		site_id        = COALESCE(return_line_facts.site_id, EXCLUDED.site_id),
		sku            = COALESCE(return_line_facts.sku, EXCLUDED.sku),
		quantity       = COALESCE(return_line_facts.quantity, EXCLUDED.quantity),
		rma_id         = COALESCE(return_line_facts.rma_id, EXCLUDED.rma_id),
		disposition_id = COALESCE(return_line_facts.disposition_id, EXCLUDED.disposition_id),
		opened_at      = LEAST(return_line_facts.opened_at, EXCLUDED.opened_at),
		inspected_at   = LEAST(return_line_facts.inspected_at, EXCLUDED.inspected_at),
		decided_at     = LEAST(return_line_facts.decided_at, EXCLUDED.decided_at),
		staged_at      = LEAST(return_line_facts.staged_at, EXCLUDED.staged_at)`

const insertDispositionSQL = `
	INSERT INTO disposition_facts (disposition_id, receipt_id, line_no, sku, quantity, grade, outcome, policy_version,
	                               supersedes, reason, decided_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::timestamptz)
	ON CONFLICT (disposition_id) DO NOTHING`

// lineCandidate is the per-line contribution of one event: which descriptive
// columns and milestone candidates it offers (nil/zero = none).
type lineCandidate struct {
	siteID        string
	sku           string
	quantity      int64
	rmaID         string
	dispositionID string
	m             report.Milestones
}

// candidates maps one event to its per-line contributions: the opened event
// contributes one per carried line, every other kind exactly one.
func candidates(e report.Event) []lineCandidate {
	ms := report.MilestonesOf(e)
	if e.Kind == report.KindReturnReceiptOpened {
		out := make([]lineCandidate, 0, len(e.Lines))
		for _, l := range e.Lines {
			out = append(out, lineCandidate{
				siteID: e.SiteID, sku: l.SKU, quantity: l.Quantity, rmaID: l.RMAID, m: ms,
			})
		}
		return out
	}
	c := lineCandidate{sku: e.SKU, quantity: e.Quantity, m: ms}
	switch e.Kind {
	case report.KindUnexpectedReturnReceived:
		c.siteID = e.SiteID
	case report.KindDispositionDecided:
		c.dispositionID = e.DispositionID
	case report.KindReturnLineStaged:
		c.siteID, c.rmaID, c.dispositionID = e.SiteID, e.RMAID, e.DispositionID
	}
	return []lineCandidate{c}
}

// Apply implements report.Projection.
func (p *Projection) Apply(ctx context.Context, e report.Event) (bool, error) {
	if err := e.Validate(); err != nil {
		// A pure check of the decoded event: deterministic, the same bytes
		// always fail (the consumer dead-letters it).
		return false, fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("analyticsstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	tag, err := tx.Exec(ctx, claimSQL, e.EventID, string(e.Kind), e.At.UTC())
	if err != nil {
		return false, classify(fmt.Errorf("analyticsstore: claim event %s: %w", e.EventID, err))
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already applied: the deferred rollback ends the empty tx
	}
	if err := fold(ctx, tx, e); err != nil {
		return false, classify(fmt.Errorf("analyticsstore: project %s %s: %w", e.Kind, e.EventID, err))
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("analyticsstore: commit: %w", err)
	}
	return true, nil
}

// fold writes the facts of one event inside tx.
func fold(ctx context.Context, tx pgx.Tx, e report.Event) error {
	if e.Kind == report.KindDispositionDecided {
		supersedes, reason := nullable(e.Supersedes), nullable(e.Reason)
		if _, err := tx.Exec(ctx, insertDispositionSQL, e.DispositionID, e.ReceiptID, e.LineNo, e.SKU, e.Quantity,
			e.Grade, e.Outcome, e.PolicyVersion, supersedes, reason, e.At.UTC()); err != nil {
			return err
		}
	}
	lines := report.LinesOf(e)
	cs := candidates(e)
	for i, c := range cs {
		if _, err := tx.Exec(ctx, upsertLineSQL, lines[i].ReceiptID, lines[i].LineNo,
			nullable(c.siteID), nullable(c.sku), nullableQty(c.quantity), nullable(c.rmaID), nullable(c.dispositionID),
			c.m.Opened, c.m.Inspected, c.m.Decided, c.m.Staged); err != nil {
			return err
		}
	}
	return nil
}

// nullable maps the domain's "" absence to SQL NULL for the genuinely
// nullable descriptive columns.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableQty maps a zero quantity (the event carries none for the line) to
// SQL NULL so COALESCE keeps the stored value.
func nullableQty(q int64) any {
	if q == 0 {
		return nil
	}
	return q
}

// classify wraps a Postgres data-exception (SQLSTATE class 22) or
// integrity-violation (class 23) error in report.ErrRejected: the same event
// can never succeed, so the consumer dead-letters it instead of retrying
// forever. Everything else (connection loss, timeouts, locks, failovers) stays
// transient.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) >= 2 && (pgErr.Code[:2] == "22" || pgErr.Code[:2] == "23") {
		return fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	return err
}
