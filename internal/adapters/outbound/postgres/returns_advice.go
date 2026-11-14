package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// ReturnAdvices is the pgxpool-backed advice view of ADR 0012: the
// poller-fed local read model of the network's return authorizations plus
// the outcome submission state. The network-owned columns are upserted by
// the poller; the outcome columns are written by the completion hook and
// the drain, and the two families never meet in one write.
type ReturnAdvices struct {
	pool *pgxpool.Pool
}

// NewReturnAdvices constructs the advice view over pool.
func NewReturnAdvices(pool *pgxpool.Pool) *ReturnAdvices { return &ReturnAdvices{pool: pool} }

// adviceLineJSON is the stored shape of one authorized line.
type adviceLineJSON struct {
	LineNo        int32  `json:"lineNo"`
	CatalogItemID string `json:"catalogItemId"`
	Quantity      int64  `json:"quantity"`
	OwnerRef      string `json:"ownerRef"`
}

// outcomeLineJSON is the stored shape of one outcome line.
type outcomeLineJSON struct {
	CatalogItemID  string `json:"catalogItemId"`
	OwnerRef       string `json:"ownerRef,omitempty"`
	QtyRestockable int64  `json:"qtyRestockable"`
	QtyUnsellable  int64  `json:"qtyUnsellable"`
}

// ApplyReturnAdvice implements ports.ReturnAdviceFeed: first sight inserts,
// re-delivery refreshes the network-owned facts; the outcome columns are
// never touched (a NULL outcome stays NULL, a stored one survives).
func (m *ReturnAdvices) ApplyReturnAdvice(ctx context.Context, a views.ReturnAdvice) error {
	lines, err := json.Marshal(adviceLinesOf(a))
	if err != nil {
		return fmt.Errorf("apply return advice %s: %w", a.AuthorizationID, err)
	}
	tracking, err := json.Marshal(a.TrackingNumbers)
	if err != nil {
		return fmt.Errorf("apply return advice %s: %w", a.AuthorizationID, err)
	}
	_, err = queryFor(ctx, m.pool).Exec(ctx, `
		INSERT INTO return_advice_view
			(authorization_id, order_id, group_no, vendor_id, node_id, disposition, method,
			 state, lines, tracking_numbers, ready_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (authorization_id) DO UPDATE SET
			order_id = EXCLUDED.order_id, group_no = EXCLUDED.group_no,
			vendor_id = EXCLUDED.vendor_id, node_id = EXCLUDED.node_id,
			disposition = EXCLUDED.disposition, method = EXCLUDED.method,
			state = EXCLUDED.state, lines = EXCLUDED.lines,
			tracking_numbers = EXCLUDED.tracking_numbers,
			ready_at = EXCLUDED.ready_at, expires_at = EXCLUDED.expires_at,
			updated_at = now()
	`, a.AuthorizationID, a.OrderID, a.GroupNo, a.VendorID, a.NodeID, a.Disposition, a.Method,
		a.State, lines, tracking, a.ReadyAt.UTC(), nullTime(a.ExpiresAt))
	if err != nil {
		return fmt.Errorf("apply return advice %s: %w", a.AuthorizationID, err)
	}
	return nil
}

// MarkOutcomePending implements ports.ReturnAdviceFeed: the guarded insert
// of the composed outcome — exactly once per advice.
func (m *ReturnAdvices) MarkOutcomePending(ctx context.Context, authorizationID string, out views.ReturnOutcome) error {
	lines, err := json.Marshal(outcomeLinesOf(out))
	if err != nil {
		return fmt.Errorf("mark outcome pending %s: %w", authorizationID, err)
	}
	tag, err := queryFor(ctx, m.pool).Exec(ctx, `
		UPDATE return_advice_view SET
			outcome_state = 'PENDING', outcome_node_id = $2, outcome_received_at = $3,
			outcome_lines = $4, outcome_since = now()
		WHERE authorization_id = $1 AND outcome_state IS NULL
	`, authorizationID, out.NodeID, out.ReceivedAt.UTC(), lines)
	if err != nil {
		return fmt.Errorf("mark outcome pending %s: %w", authorizationID, err)
	}
	if tag.RowsAffected() == 0 {
		return m.pendingMiss(ctx, authorizationID)
	}
	return nil
}

// MarkOutcomeSubmitted implements ports.ReturnAdviceFeed.
func (m *ReturnAdvices) MarkOutcomeSubmitted(ctx context.Context, authorizationID, transactionID string) error {
	tag, err := queryFor(ctx, m.pool).Exec(ctx, `
		UPDATE return_advice_view SET outcome_state = 'SUBMITTED', outcome_transaction_id = $2
		WHERE authorization_id = $1 AND outcome_state IN ('PENDING', 'SUBMITTED')
	`, authorizationID, transactionID)
	if err != nil {
		return fmt.Errorf("mark outcome submitted %s: %w", authorizationID, err)
	}
	if tag.RowsAffected() == 0 {
		return m.outcomeMiss(ctx, authorizationID)
	}
	return nil
}

// MarkOutcomeSucceeded implements ports.ReturnAdviceFeed (terminal,
// idempotent: a replayed success verdict changes nothing).
func (m *ReturnAdvices) MarkOutcomeSucceeded(ctx context.Context, authorizationID string) error {
	tag, err := queryFor(ctx, m.pool).Exec(ctx, `
		UPDATE return_advice_view SET outcome_state = 'SUCCEEDED'
		WHERE authorization_id = $1 AND outcome_state IN ('PENDING', 'SUBMITTED')
	`, authorizationID)
	if err != nil {
		return fmt.Errorf("mark outcome succeeded %s: %w", authorizationID, err)
	}
	if tag.RowsAffected() == 0 {
		return m.outcomeMiss(ctx, authorizationID)
	}
	return nil
}

// MarkOutcomeFailed implements ports.ReturnAdviceFeed (terminal).
func (m *ReturnAdvices) MarkOutcomeFailed(ctx context.Context, authorizationID, refusal string) error {
	tag, err := queryFor(ctx, m.pool).Exec(ctx, `
		UPDATE return_advice_view SET outcome_state = 'FAILED', outcome_errors = $2
		WHERE authorization_id = $1 AND outcome_state IN ('PENDING', 'SUBMITTED')
	`, authorizationID, refusal)
	if err != nil {
		return fmt.Errorf("mark outcome failed %s: %w", authorizationID, err)
	}
	if tag.RowsAffected() == 0 {
		return m.outcomeMiss(ctx, authorizationID)
	}
	return nil
}

// Get implements ports.ReturnAdviceViews.
func (m *ReturnAdvices) Get(ctx context.Context, authorizationID string) (views.ReturnAdvice, bool, error) {
	var (
		a        views.ReturnAdvice
		lines    []byte
		tracking []byte
		expires  *time.Time
		state    *string
	)
	err := queryFor(ctx, m.pool).QueryRow(ctx, `
		SELECT authorization_id, order_id, group_no, vendor_id, node_id, disposition, method,
			state, lines, tracking_numbers, ready_at, expires_at, first_seen_at, outcome_state
		FROM return_advice_view WHERE authorization_id = $1
	`, authorizationID).Scan(&a.AuthorizationID, &a.OrderID, &a.GroupNo, &a.VendorID, &a.NodeID,
		&a.Disposition, &a.Method, &a.State, &lines, &tracking, &a.ReadyAt, &expires,
		&a.FirstSeenAt, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return views.ReturnAdvice{}, false, nil
	}
	if err != nil {
		return views.ReturnAdvice{}, false, fmt.Errorf("get return advice %s: %w", authorizationID, err)
	}
	var stored []adviceLineJSON
	if err := json.Unmarshal(lines, &stored); err != nil {
		return views.ReturnAdvice{}, false, fmt.Errorf("get return advice %s: %w", authorizationID, err)
	}
	if err := json.Unmarshal(tracking, &a.TrackingNumbers); err != nil {
		return views.ReturnAdvice{}, false, fmt.Errorf("get return advice %s: %w", authorizationID, err)
	}
	for _, l := range stored {
		a.Lines = append(a.Lines, views.ReturnAdviceLine{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, Quantity: l.Quantity, OwnerRef: l.OwnerRef})
	}
	a.ExpiresAt, a.OutcomeState = timeOrNull(expires), derefString(state)
	return a, true, nil
}

// PendingOutcomes implements ports.ReturnAdviceViews: PENDING and SUBMITTED
// outcomes, oldest first (the drain's work list), bounded per tick.
func (m *ReturnAdvices) PendingOutcomes(ctx context.Context) ([]views.PendingReturnOutcome, error) {
	rows, err := queryFor(ctx, m.pool).Query(ctx, `
		SELECT authorization_id, outcome_node_id, outcome_received_at, outcome_lines,
			outcome_transaction_id, outcome_since
		FROM return_advice_view
		WHERE outcome_state IN ('PENDING', 'SUBMITTED')
		ORDER BY outcome_since ASC
		LIMIT 500
	`)
	if err != nil {
		return nil, fmt.Errorf("pending outcomes: %w", err)
	}
	defer rows.Close()
	var out []views.PendingReturnOutcome
	for rows.Next() {
		var (
			p     views.PendingReturnOutcome
			lines []byte
			txID  *string
		)
		if err := rows.Scan(&p.Outcome.AuthorizationID, &p.Outcome.NodeID, &p.Outcome.ReceivedAt,
			&lines, &txID, &p.PendingSince); err != nil {
			return nil, fmt.Errorf("pending outcomes: %w", err)
		}
		if txID != nil {
			p.TransactionID = *txID
		}
		var stored []outcomeLineJSON
		if err := json.Unmarshal(lines, &stored); err != nil {
			return nil, fmt.Errorf("pending outcomes: %w", err)
		}
		for _, l := range stored {
			p.Outcome.Lines = append(p.Outcome.Lines, views.ReturnOutcomeLine{
				CatalogItemID: l.CatalogItemID, OwnerRef: l.OwnerRef,
				QtyRestockable: l.QtyRestockable, QtyUnsellable: l.QtyUnsellable,
			})
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LatestReadyAt implements ports.ReturnAdviceViews (the poll watermark).
func (m *ReturnAdvices) LatestReadyAt(ctx context.Context) (time.Time, bool, error) {
	var latest *time.Time
	if err := queryFor(ctx, m.pool).QueryRow(ctx, `
		SELECT max(ready_at) FROM return_advice_view
	`).Scan(&latest); err != nil {
		return time.Time{}, false, fmt.Errorf("latest ready at: %w", err)
	}
	if latest == nil {
		return time.Time{}, false, nil
	}
	return latest.UTC(), true, nil
}

// pendingMiss classifies a guarded MarkOutcomePending that changed nothing.
func (m *ReturnAdvices) pendingMiss(ctx context.Context, authorizationID string) error {
	known, err := m.exists(ctx, authorizationID)
	if err != nil {
		return err
	}
	if !known {
		return ports.ErrAdviceUnknown
	}
	return ports.ErrOutcomeAlreadyStored
}

// outcomeMiss classifies a terminal-marking write that changed nothing.
func (m *ReturnAdvices) outcomeMiss(ctx context.Context, authorizationID string) error {
	known, err := m.exists(ctx, authorizationID)
	if err != nil {
		return err
	}
	if !known {
		return ports.ErrAdviceUnknown
	}
	return nil // the outcome is already terminal: a replayed verdict
}

func (m *ReturnAdvices) exists(ctx context.Context, authorizationID string) (bool, error) {
	var one int
	err := queryFor(ctx, m.pool).QueryRow(ctx,
		`SELECT 1 FROM return_advice_view WHERE authorization_id = $1`, authorizationID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("advice exists %s: %w", authorizationID, err)
	}
	return true, nil
}

// derefString maps SQL NULL back to "".
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func adviceLinesOf(a views.ReturnAdvice) []adviceLineJSON {
	out := make([]adviceLineJSON, len(a.Lines))
	for i, l := range a.Lines {
		out[i] = adviceLineJSON{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, Quantity: l.Quantity, OwnerRef: l.OwnerRef}
	}
	return out
}

func outcomeLinesOf(out views.ReturnOutcome) []outcomeLineJSON {
	stored := make([]outcomeLineJSON, len(out.Lines))
	for i, l := range out.Lines {
		stored[i] = outcomeLineJSON{CatalogItemID: l.CatalogItemID, OwnerRef: l.OwnerRef, QtyRestockable: l.QtyRestockable, QtyUnsellable: l.QtyUnsellable}
	}
	return stored
}
