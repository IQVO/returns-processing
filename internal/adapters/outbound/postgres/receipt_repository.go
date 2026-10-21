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
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// Receipts is the pgxpool-backed ports.ReturnReceiptRepo: the root row plus
// one row per line, written together in the caller's transaction when there
// is one.
type Receipts struct {
	pool *pgxpool.Pool
}

// NewReceipts constructs a Receipts repo over pool.
func NewReceipts(pool *pgxpool.Pool) *Receipts { return &Receipts{pool: pool} }

const receiptColumns = `receipt_id, rma_id, site_id, opened_at, version`

// Save persists the receipt atomically: the version-guarded root upsert
// first (zero rows affected is ports.ErrConcurrentModification), then the
// lines replaced wholesale — inspect only ever adds facts to existing lines,
// so the delete+insert of the current snapshot is exactly the delta.
func (r *Receipts) Save(ctx context.Context, rc *returnreceipt.Receipt) error {
	s := rc.Snapshot()
	return inTx(ctx, r.pool, func(q querier) error {
		tag, err := q.Exec(ctx, `
			INSERT INTO return_receipts (`+receiptColumns+`)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (receipt_id) DO UPDATE
			SET rma_id = EXCLUDED.rma_id, site_id = EXCLUDED.site_id,
			    opened_at = EXCLUDED.opened_at, version = EXCLUDED.version, updated_at = now()
			WHERE return_receipts.version = $6
		`, s.ID, nullString(s.RMAID), s.SiteID, s.OpenedAt.UTC(), s.Version, s.Version-1)
		if err != nil {
			return fmt.Errorf("save receipt %s: %w", s.ID, err)
		}
		if tag.RowsAffected() == 0 {
			return ports.ErrConcurrentModification
		}
		if _, err := q.Exec(ctx, `DELETE FROM return_receipt_lines WHERE receipt_id = $1`, s.ID); err != nil {
			return fmt.Errorf("replace lines of receipt %s: %w", s.ID, err)
		}
		for _, l := range s.Lines {
			if _, err := q.Exec(ctx, `
				INSERT INTO return_receipt_lines
					(receipt_id, line_no, sku, quantity, kind, rma_id, inspected,
					 inspected_quantity, grade, reason_codes, inspected_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			`, s.ID, l.LineNo, l.SKU, l.Quantity, string(l.Kind), nullString(l.RMAID),
				l.Inspected, l.InspectedQuantity, nullString(string(l.Grade)),
				reasonCodesJSON(l.ReasonCodes), nullTime(l.InspectedAt)); err != nil {
				return fmt.Errorf("save line %d of receipt %s: %w", l.LineNo, s.ID, err)
			}
		}
		return nil
	})
}

// reasonCodesJSON stores the inspection reasons as a JSON array; nil and
// empty both store [] (the column default).
func reasonCodesJSON(reasons []string) []byte {
	if len(reasons) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(reasons)
	if err != nil {
		return []byte("[]")
	}
	return b
}

// FindByID implements ports.ReturnReceiptRepo: the root plus its lines, by
// line number.
func (r *Receipts) FindByID(ctx context.Context, id string) (*returnreceipt.Receipt, error) {
	q := queryFor(ctx, r.pool)
	s, err := scanReceipt(q.QueryRow(ctx,
		`SELECT `+receiptColumns+` FROM return_receipts WHERE receipt_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		SELECT line_no, sku, quantity, kind, rma_id, inspected,
		       inspected_quantity, grade, reason_codes, inspected_at
		FROM return_receipt_lines
		WHERE receipt_id = $1
		ORDER BY line_no
	`, id)
	if err != nil {
		return nil, fmt.Errorf("load lines of receipt %s: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			l       returnreceipt.LineSnapshot
			kind    string
			rma     *string
			grade   *string
			reasons []byte
			inspAt  *time.Time
		)
		if err := rows.Scan(&l.LineNo, &l.SKU, &l.Quantity, &kind, &rma, &l.Inspected,
			&l.InspectedQuantity, &grade, &reasons, &inspAt); err != nil {
			return nil, fmt.Errorf("scan line of receipt %s: %w", id, err)
		}
		l.Kind = returnreceipt.LineKind(kind)
		l.RMAID = stringOrNull(rma)
		l.Grade = shared.Grade(stringOrNull(grade))
		l.InspectedAt = timeOrNull(inspAt)
		l.ReasonCodes = unmarshalReasons(reasons)
		s.Lines = append(s.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rc, err := returnreceipt.Restore(s)
	if err != nil {
		return nil, fmt.Errorf("stored receipt %s is invalid: %w", id, err)
	}
	return rc, nil
}

func scanReceipt(row pgx.Row) (returnreceipt.Snapshot, error) {
	var (
		s   returnreceipt.Snapshot
		rma *string
	)
	err := row.Scan(&s.ID, &rma, &s.SiteID, &s.OpenedAt, &s.Version)
	s.RMAID = stringOrNull(rma)
	s.OpenedAt = s.OpenedAt.UTC()
	return s, err
}

func unmarshalReasons(b []byte) []string {
	var reasons []string
	if err := json.Unmarshal(b, &reasons); err != nil {
		return nil
	}
	return reasons
}
