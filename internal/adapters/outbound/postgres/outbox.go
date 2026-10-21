package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
)

// Aggregate type segments of the outbox rows — the same segments the
// CloudEvents `type` is built from (ADR 0004).
const (
	aggregRMA         = "returnauthorization"
	aggregReceipt     = "returnreceipt"
	aggregDisposition = "disposition"
)

// Outbox is the pgxpool-backed ports.EventPublisher: the write side of the
// transactional outbox. Every published domain fact becomes one outbox row
// in the SAME transaction as the aggregate's Save (the UnitOfWork ctx), so a
// state change and its facts commit or roll back together. The relay (a
// later step) drains due rows to Kafka as CloudEvents.
//
// Each row's payload is a self-describing JSON object {"type": ..., "data":
// ...} where data is ALREADY the pinned CloudEvents data of ADR 0004 / the
// contracts (snake_case field names, RFC3339 instants); the relay derives
// type/subject/dataschema from aggreg_type + payload.type. event_id is the
// CloudEvents id, minted here once and persisted: a relay retry republishes
// the same id, which is what lets consumers dedupe.
type Outbox struct {
	pool *pgxpool.Pool
}

// NewOutbox constructs an Outbox over pool.
func NewOutbox(pool *pgxpool.Pool) *Outbox { return &Outbox{pool: pool} }

// outboxEnvelope is the persisted payload shape.
type outboxEnvelope struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

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
		data, err := rmaEventData(e)
		if err != nil {
			return err
		}
		rows = append(rows, row{
			eventID: uuid.NewString(), aggregType: aggregRMA, aggregID: e.RMAID(),
			payload: data, occurredAt: e.OccurredAt(),
		})
	}
	return o.insert(ctx, rows)
}

// PublishReceiptEvents implements ports.EventPublisher.
func (o *Outbox) PublishReceiptEvents(ctx context.Context, events ...returnreceipt.Event) error {
	rows := make([]row, 0, len(events))
	for _, e := range events {
		data, err := receiptEventData(e)
		if err != nil {
			return err
		}
		rows = append(rows, row{
			eventID: uuid.NewString(), aggregType: aggregReceipt, aggregID: e.ReceiptID(),
			payload: data, occurredAt: e.OccurredAt(),
		})
	}
	return o.insert(ctx, rows)
}

// PublishDispositionEvents implements ports.EventPublisher.
func (o *Outbox) PublishDispositionEvents(ctx context.Context, events ...disposition.Event) error {
	rows := make([]row, 0, len(events))
	for _, e := range events {
		data, err := dispositionEventData(e)
		if err != nil {
			return err
		}
		rows = append(rows, row{
			eventID: uuid.NewString(), aggregType: aggregDisposition, aggregID: e.DispositionID(),
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

// envelope marshals one event's payload.
func envelope(eventName string, data any) ([]byte, error) {
	b, err := json.Marshal(outboxEnvelope{Type: eventName, Data: data})
	if err != nil {
		return nil, fmt.Errorf("encode %s payload: %w", eventName, err)
	}
	return b, nil
}

// rmaEventData maps an RMA fact to the pinned CloudEvents data (ADR 0004).
func rmaEventData(e returnauthorization.Event) ([]byte, error) {
	switch ev := e.(type) {
	case returnauthorization.ReturnAuthorized:
		return envelope(ev.EventName(), map[string]any{
			"rma_id":         ev.RMA,
			"order_id":       ev.OrderRef.OrderID().String(),
			"line_no":        ev.OrderRef.LineNo(),
			"sku":            ev.SKU,
			"quantity":       ev.Quantity,
			"site_id":        ev.Site,
			"reason_code":    ev.ReasonCode,
			"window_ends_at": ev.WindowEnds.Format(time.RFC3339),
			"authorized_at":  ev.Authorized.Format(time.RFC3339),
		})
	case returnauthorization.ReturnAuthorizationRejected:
		return envelope(ev.EventName(), map[string]any{
			"rma_id":         ev.RMA,
			"order_id":       ev.OrderRef.OrderID().String(),
			"line_no":        ev.OrderRef.LineNo(),
			"sku":            ev.SKU,
			"quantity":       ev.Quantity,
			"rejection_code": string(ev.RejectionCode),
			"rejected_at":    ev.RejectedAt.Format(time.RFC3339),
		})
	default:
		return nil, fmt.Errorf("unknown rma event %T", e)
	}
}

// receiptEventData maps a receipt fact to the pinned data.
func receiptEventData(e returnreceipt.Event) ([]byte, error) {
	switch ev := e.(type) {
	case returnreceipt.ReturnReceiptOpened:
		lines := make([]map[string]any, 0, len(ev.Lines))
		for _, l := range ev.Lines {
			lines = append(lines, map[string]any{
				"line_no":  l.LineNo,
				"sku":      l.SKU,
				"quantity": l.Quantity,
				"rma_id":   l.RMAID,
			})
		}
		return envelope(ev.EventName(), map[string]any{
			"receipt_id": ev.Receipt,
			"rma_id":     ev.RMAID,
			"site_id":    ev.SiteID,
			"opened_at":  ev.Opened.Format(time.RFC3339),
			"lines":      lines,
		})
	case returnreceipt.ReturnLineInspected:
		return envelope(ev.EventName(), map[string]any{
			"receipt_id":   ev.Receipt,
			"line_no":      ev.LineNo,
			"sku":          ev.SKU,
			"quantity":     ev.Quantity,
			"grade":        ev.Grade,
			"reason_codes": ev.ReasonCodes,
			"inspected_at": ev.Inspected.Format(time.RFC3339),
		})
	case returnreceipt.UnexpectedReturnReceived:
		return envelope(ev.EventName(), map[string]any{
			"receipt_id":  ev.Receipt,
			"line_no":     ev.LineNo,
			"sku":         ev.SKU,
			"quantity":    ev.Quantity,
			"site_id":     ev.SiteID,
			"received_at": ev.Received.Format(time.RFC3339),
		})
	default:
		return nil, fmt.Errorf("unknown receipt event %T", e)
	}
}

// dispositionEventData maps a disposition fact to the pinned data.
func dispositionEventData(e disposition.Event) ([]byte, error) {
	switch ev := e.(type) {
	case disposition.DispositionDecided:
		return envelope(ev.EventName(), map[string]any{
			"disposition_id": ev.Disposition,
			"receipt_id":     ev.ReceiptID,
			"line_no":        ev.LineNo,
			"sku":            ev.SKU,
			"quantity":       ev.Quantity,
			"grade":          ev.Grade,
			"outcome":        string(ev.Outcome),
			"policy_version": ev.PolicyVersion,
			"supersedes":     ev.Supersedes,
			"reason":         ev.Reason,
			"decided_at":     ev.DecidedAt.Format(time.RFC3339),
		})
	case disposition.ReturnLineStaged:
		return envelope(ev.EventName(), map[string]any{
			"receipt_id":     ev.ReceiptID,
			"line_no":        ev.LineNo,
			"sku":            ev.SKU,
			"quantity":       ev.Quantity,
			"condition":      ev.Condition,
			"received_at":    ev.ReceivedAt.Format(time.RFC3339),
			"rma_id":         ev.RMAID,
			"disposition_id": ev.Disposition,
			"site_id":        ev.SiteID,
		})
	default:
		return nil, fmt.Errorf("unknown disposition event %T", e)
	}
}
