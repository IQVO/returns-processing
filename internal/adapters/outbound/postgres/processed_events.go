package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProcessedEvents is the pgxpool-backed ports.ProcessedEvents: the inbound
// consumers' id-dedupe guard. processed_events holds one row per CloudEvents
// id; the row is claimed with INSERT ... ON CONFLICT DO NOTHING in the SAME
// transaction as the effect (the UnitOfWork the caller opened carries it in
// ctx), so a rollback un-claims the id and the redelivery is processed.
type ProcessedEvents struct {
	pool *pgxpool.Pool
}

// NewProcessedEvents constructs a ProcessedEvents guard over pool.
func NewProcessedEvents(pool *pgxpool.Pool) *ProcessedEvents {
	return &ProcessedEvents{pool: pool}
}

// Claim implements ports.ProcessedEvents. Called inside a UnitOfWork it
// joins that transaction; called bare it commits on its own.
func (p *ProcessedEvents) Claim(ctx context.Context, eventID string) (bool, error) {
	tag, err := queryFor(ctx, p.pool).Exec(ctx, `
		INSERT INTO processed_events (event_id) VALUES ($1)
		ON CONFLICT (event_id) DO NOTHING
	`, eventID)
	if err != nil {
		return false, fmt.Errorf("claim processed event %s: %w", eventID, err)
	}
	return tag.RowsAffected() == 1, nil
}
