package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/application/outbox"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
)

// Outbox is the pgxpool-backed ports.EventPublisher: the write side of the
// transactional outbox. Every published domain fact becomes one outbox row
// in the SAME transaction as the aggregate's Save (the UnitOfWork ctx), so a
// state change and its facts commit or roll back together. The relay (see
// outbox_relay.go) drains due rows to Kafka as CloudEvents.
//
// Each row's payload is the self-describing envelope built by
// internal/application/outbox (the ONE mapping the golden tests pin):
// {"type": <EventName>, "data": ...} where data is ALREADY the pinned
// CloudEvents data of ADR 0004. event_id is the CloudEvents id, minted here
// once and persisted: a relay retry republishes the same id, which is what
// lets consumers dedupe.
type Outbox struct {
	pool *pgxpool.Pool
}

// NewOutbox constructs an Outbox over pool.
func NewOutbox(pool *pgxpool.Pool) *Outbox { return &Outbox{pool: pool} }

// row is one prepared outbox insert.
type row struct {
	eventID    string
	aggregType string
	aggregID   string
	payload    []byte
	occurredAt time.Time
}

// PublishRMAEvents implements ports.EventPublisher.
func (o *Outbox) PublishRMAEvents(ctx context.Context, events ...returnauthorization.Event) error {
	rows := make([]row, 0, len(events))
	for _, e := range events {
		data, err := outbox.RMAPayload(e)
		if err != nil {
			return err
		}
		rows = append(rows, row{
			eventID: uuid.NewString(), aggregType: outbox.AggregRMA, aggregID: e.RMAID(),
			payload: data, occurredAt: e.OccurredAt(),
		})
	}
	return o.insert(ctx, rows)
}

// PublishReceiptEvents implements ports.EventPublisher.
func (o *Outbox) PublishReceiptEvents(ctx context.Context, events ...returnreceipt.Event) error {
	rows := make([]row, 0, len(events))
	for _, e := range events {
		data, err := outbox.ReceiptPayload(e)
		if err != nil {
			return err
		}
		rows = append(rows, row{
			eventID: uuid.NewString(), aggregType: outbox.AggregReceipt, aggregID: e.ReceiptID(),
			payload: data, occurredAt: e.OccurredAt(),
		})
	}
	return o.insert(ctx, rows)
}

// PublishDispositionEvents implements ports.EventPublisher.
func (o *Outbox) PublishDispositionEvents(ctx context.Context, events ...disposition.Event) error {
	rows := make([]row, 0, len(events))
	for _, e := range events {
		data, err := outbox.DispositionPayload(e)
		if err != nil {
			return err
		}
		rows = append(rows, row{
			eventID: uuid.NewString(), aggregType: outbox.AggregDisposition, aggregID: e.DispositionID(),
			payload: data, occurredAt: e.OccurredAt(),
		})
	}
	return o.insert(ctx, rows)
}

// insert writes the rows on the caller's transaction when there is one,
// otherwise in one transaction of its own.
func (o *Outbox) insert(ctx context.Context, rows []row) error {
	if len(rows) == 0 {
		return nil
	}
	return inTx(ctx, o.pool, func(q querier) error {
		for _, r := range rows {
			if _, err := q.Exec(ctx, `
				INSERT INTO outbox (event_id, aggreg_type, aggreg_id, payload, occurred_at)
				VALUES ($1, $2, $3, $4, $5)
			`, r.eventID, r.aggregType, r.aggregID, r.payload, r.occurredAt.UTC()); err != nil {
				return fmt.Errorf("enqueue outbox %s row: %w", r.aggregType, err)
			}
		}
		return nil
	})
}
