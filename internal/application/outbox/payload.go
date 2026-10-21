package outbox

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
)

// envelope is the persisted payload shape of every outbox row: the domain
// event's name plus its ALREADY-pinned CloudEvents data (ADR 0004 /
// .claude/rules/integration-events.md — snake_case field names, RFC 3339
// instants, JSON null for an absent nullable field).
type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// nullable renders a nullable payload field: JSON null when the value is the
// empty string, the value otherwise. The pinned contracts mark rma_id,
// supersedes and reason as (nullable); the domain represents absence as "",
// which must never reach the wire as a zero-length id or reason.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// build marshals one event's payload.
func build(eventName string, data any) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode %s payload: %w", eventName, err)
	}
	b, err := json.Marshal(envelope{Type: eventName, Data: raw})
	if err != nil {
		return nil, fmt.Errorf("encode %s payload: %w", eventName, err)
	}
	return b, nil
}

// RMAPayload maps an RMA fact to its persisted outbox payload. An event type
// the service does not publish is a programming error.
func RMAPayload(e returnauthorization.Event) ([]byte, error) {
	switch ev := e.(type) {
	case returnauthorization.ReturnAuthorized:
		return build(ev.EventName(), map[string]any{
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
		return build(ev.EventName(), map[string]any{
			"rma_id":         ev.RMA,
			"order_id":       ev.OrderRef.OrderID().String(),
			"line_no":        ev.OrderRef.LineNo(),
			"sku":            ev.SKU,
			"quantity":       ev.Quantity,
			"rejection_code": string(ev.RejectionCode),
			"rejected_at":    ev.RejectedAt.Format(time.RFC3339),
		})
	default:
		return nil, fmt.Errorf("outbox: unknown rma event %T", e)
	}
}

// ReceiptPayload maps a receipt fact to its persisted payload. rma_id is null
// when the parcel (or the line) was unexpected.
func ReceiptPayload(e returnreceipt.Event) ([]byte, error) {
	switch ev := e.(type) {
	case returnreceipt.ReturnReceiptOpened:
		lines := make([]map[string]any, 0, len(ev.Lines))
		for _, l := range ev.Lines {
			lines = append(lines, map[string]any{
				"line_no":  l.LineNo,
				"sku":      l.SKU,
				"quantity": l.Quantity,
				"rma_id":   nullable(l.RMAID),
			})
		}
		return build(ev.EventName(), map[string]any{
			"receipt_id": ev.Receipt,
			"rma_id":     nullable(ev.RMAID),
			"site_id":    ev.SiteID,
			"opened_at":  ev.Opened.Format(time.RFC3339),
			"lines":      lines,
		})
	case returnreceipt.ReturnLineInspected:
		return build(ev.EventName(), map[string]any{
			"receipt_id":   ev.Receipt,
			"line_no":      ev.LineNo,
			"sku":          ev.SKU,
			"quantity":     ev.Quantity,
			"grade":        ev.Grade,
			"reason_codes": ev.ReasonCodes,
			"inspected_at": ev.Inspected.Format(time.RFC3339),
		})
	case returnreceipt.UnexpectedReturnReceived:
		return build(ev.EventName(), map[string]any{
			"receipt_id":  ev.Receipt,
			"line_no":     ev.LineNo,
			"sku":         ev.SKU,
			"quantity":    ev.Quantity,
			"site_id":     ev.SiteID,
			"received_at": ev.Received.Format(time.RFC3339),
		})
	default:
		return nil, fmt.Errorf("outbox: unknown receipt event %T", e)
	}
}

// DispositionPayload maps a disposition fact to its persisted payload.
// supersedes and reason are null on a policy decision; an override carries
// both.
func DispositionPayload(e disposition.Event) ([]byte, error) {
	switch ev := e.(type) {
	case disposition.DispositionDecided:
		return build(ev.EventName(), map[string]any{
			"disposition_id": ev.Disposition,
			"receipt_id":     ev.ReceiptID,
			"line_no":        ev.LineNo,
			"sku":            ev.SKU,
			"quantity":       ev.Quantity,
			"grade":          ev.Grade,
			"outcome":        string(ev.Outcome),
			"policy_version": ev.PolicyVersion,
			"supersedes":     nullable(ev.Supersedes),
			"reason":         nullable(ev.Reason),
			"decided_at":     ev.DecidedAt.Format(time.RFC3339),
		})
	case disposition.ReturnLineStaged:
		return build(ev.EventName(), map[string]any{
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
		return nil, fmt.Errorf("outbox: unknown disposition event %T", e)
	}
}
