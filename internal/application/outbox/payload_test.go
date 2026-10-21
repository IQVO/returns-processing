package outbox_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/application/outbox"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// The persisted payload is the input of the relay-side encoder: these tests
// pin, per type, the envelope's event name and the EXACT data object of ADR
// 0004 (snake_case fields, RFC 3339 instants, JSON null for an absent
// nullable field — never a zero-length string).

func at(h, m int) time.Time { return time.Date(2026, 10, 21, h, m, 0, 0, time.UTC) }

// rmaData/receiptData/dispositionData build the payload through the
// production mapping and return (event name, data object).
func rmaData(t *testing.T, e returnauthorization.Event) (string, map[string]any) {
	t.Helper()
	b, err := outbox.RMAPayload(e)
	if err != nil {
		t.Fatalf("rma payload: %v", err)
	}
	return decode(t, b)
}

func receiptData(t *testing.T, e returnreceipt.Event) (string, map[string]any) {
	t.Helper()
	b, err := outbox.ReceiptPayload(e)
	if err != nil {
		t.Fatalf("receipt payload: %v", err)
	}
	return decode(t, b)
}

func dispositionData(t *testing.T, e disposition.Event) (string, map[string]any) {
	t.Helper()
	b, err := outbox.DispositionPayload(e)
	if err != nil {
		t.Fatalf("disposition payload: %v", err)
	}
	return decode(t, b)
}

func decode(t *testing.T, b []byte) (string, map[string]any) {
	t.Helper()
	var env struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, b)
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data is not a JSON object: %v\n%s", err, env.Data)
	}
	return env.Type, data
}

func orderLine(t *testing.T, order string, line int32) shared.OrderLineRef {
	t.Helper()
	orderID, err := shared.NewOrderID(order)
	if err != nil {
		t.Fatalf("order id: %v", err)
	}
	ref, err := shared.NewOrderLineRef(orderID, line)
	if err != nil {
		t.Fatalf("order line ref: %v", err)
	}
	return ref
}

func authorized(t *testing.T) returnauthorization.ReturnAuthorized {
	return returnauthorization.ReturnAuthorized{
		Header:   returnauthorization.Header{RMA: "rma-1", Version: 2, At: at(9, 30)},
		OrderRef: orderLine(t, "ord-7c9e", 3),
		SKU:      "SKU-BOOK-0001", Quantity: 2, Site: "site-1", ReasonCode: "DAMAGED",
		WindowEnds: at(11, 30), Authorized: at(9, 30),
	}
}

func rejected(t *testing.T) returnauthorization.ReturnAuthorizationRejected {
	return returnauthorization.ReturnAuthorizationRejected{
		Header:   returnauthorization.Header{RMA: "rma-2", Version: 2, At: at(9, 31)},
		OrderRef: orderLine(t, "ord-7c9e", 4),
		SKU:      "SKU-9", Quantity: 5,
		RejectionCode: returnauthorization.RejectLineNotManifested, RejectedAt: at(9, 31),
	}
}

func opened() returnreceipt.ReturnReceiptOpened {
	return returnreceipt.ReturnReceiptOpened{
		Header: returnreceipt.Header{Receipt: "rcpt-1", Version: 2, At: at(10, 0)},
		RMAID:  "rma-1", SiteID: "site-1", Opened: at(10, 0),
		Lines: []returnreceipt.OpenedLine{
			{LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2, RMAID: "rma-1"},
			{LineNo: 2, SKU: "SKU-X", Quantity: 1},
		},
	}
}

func inspected() returnreceipt.ReturnLineInspected {
	return returnreceipt.ReturnLineInspected{
		Header: returnreceipt.Header{Receipt: "rcpt-1", Version: 3, At: at(10, 5)},
		LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2, Grade: "A",
		ReasonCodes: []string{"like-new"}, Inspected: at(10, 5),
	}
}

func unexpectedLine() returnreceipt.UnexpectedReturnReceived {
	return returnreceipt.UnexpectedReturnReceived{
		Header: returnreceipt.Header{Receipt: "rcpt-1", Version: 2, At: at(10, 0)},
		LineNo: 2, SKU: "SKU-X", Quantity: 1, SiteID: "site-1", Received: at(10, 0),
	}
}

func decided() disposition.DispositionDecided {
	return disposition.DispositionDecided{
		Header:    disposition.Header{Disposition: "disp-1", Version: 1, At: at(10, 6)},
		ReceiptID: "rcpt-1", LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2, Grade: "A",
		Outcome: disposition.OutcomeRestock, PolicyVersion: 4, DecidedAt: at(10, 6),
	}
}

func decidedOverride() disposition.DispositionDecided {
	d := decided()
	d.Supersedes, d.Reason = "disp-1", "liquidation contract"
	return d
}

func staged() disposition.ReturnLineStaged {
	return disposition.ReturnLineStaged{
		Header:    disposition.Header{Disposition: "disp-1", Version: 2, At: at(10, 6)},
		ReceiptID: "rcpt-1", LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2,
		Condition: disposition.StagedCondition, ReceivedAt: at(10, 0),
		RMAID: "rma-1", SiteID: "site-1",
	}
}

func TestRMAPayloads(t *testing.T) {
	name, data := rmaData(t, authorized(t))
	if name != "ReturnAuthorized" {
		t.Fatalf("type = %q", name)
	}
	want := map[string]any{
		"rma_id": "rma-1", "order_id": "ord-7c9e", "line_no": float64(3),
		"sku": "SKU-BOOK-0001", "quantity": float64(2), "site_id": "site-1",
		"reason_code": "DAMAGED", "window_ends_at": "2026-10-21T11:30:00Z",
		"authorized_at": "2026-10-21T09:30:00Z",
	}
	if !mapsEqual(data, want) {
		t.Fatalf("ReturnAuthorized data = %v, want %v", data, want)
	}

	name, data = rmaData(t, rejected(t))
	if name != "ReturnAuthorizationRejected" {
		t.Fatalf("type = %q", name)
	}
	want = map[string]any{
		"rma_id": "rma-2", "order_id": "ord-7c9e", "line_no": float64(4),
		"sku": "SKU-9", "quantity": float64(5),
		"rejection_code": "LINE_NOT_MANIFESTED", "rejected_at": "2026-10-21T09:31:00Z",
	}
	if !mapsEqual(data, want) {
		t.Fatalf("ReturnAuthorizationRejected data = %v, want %v", data, want)
	}
}

func TestReceiptOpenedPayload(t *testing.T) {
	name, data := receiptData(t, opened())
	if name != "ReturnReceiptOpened" {
		t.Fatalf("type = %q", name)
	}
	if data["receipt_id"] != "rcpt-1" || data["rma_id"] != "rma-1" || data["site_id"] != "site-1" ||
		data["opened_at"] != "2026-10-21T10:00:00Z" {
		t.Fatalf("ReturnReceiptOpened scalars = %v", data)
	}
	lines, ok := data["lines"].([]any)
	if !ok || len(lines) != 2 {
		t.Fatalf("lines = %v", data["lines"])
	}
	matched, ok := lines[0].(map[string]any)
	if !ok || matched["rma_id"] != "rma-1" || matched["line_no"] != float64(1) ||
		matched["sku"] != "SKU-BOOK-0001" || matched["quantity"] != float64(2) {
		t.Fatalf("matched line = %v", lines[0])
	}
	// An UNEXPECTED line has rma_id null, never "".
	unexpected, _ := lines[1].(map[string]any)
	if v, has := unexpected["rma_id"]; !has || v != nil {
		t.Fatalf("unexpected line rma_id = %v (present=%v), want explicit null", v, has)
	}
}

// A wholly unexpected parcel has a null receipt-level rma_id.
func TestReceiptOpenedUnexpectedParcelPayload(t *testing.T) {
	o := opened()
	o.RMAID = ""
	_, data := receiptData(t, o)
	if v, has := data["rma_id"]; !has || v != nil {
		t.Fatalf("receipt rma_id = %v (present=%v), want explicit null", v, has)
	}
}

func TestLineInspectedPayload(t *testing.T) {
	name, data := receiptData(t, inspected())
	if name != "ReturnLineInspected" {
		t.Fatalf("type = %q", name)
	}
	want := map[string]any{
		"receipt_id": "rcpt-1", "line_no": float64(1), "sku": "SKU-BOOK-0001",
		"quantity": float64(2), "grade": "A",
		"reason_codes": []any{"like-new"}, "inspected_at": "2026-10-21T10:05:00Z",
	}
	if !mapsEqual(data, want) {
		t.Fatalf("ReturnLineInspected data = %v, want %v", data, want)
	}
}

func TestUnexpectedReturnPayload(t *testing.T) {
	name, data := receiptData(t, unexpectedLine())
	if name != "UnexpectedReturnReceived" {
		t.Fatalf("type = %q", name)
	}
	want := map[string]any{
		"receipt_id": "rcpt-1", "line_no": float64(2), "sku": "SKU-X",
		"quantity": float64(1), "site_id": "site-1", "received_at": "2026-10-21T10:00:00Z",
	}
	if !mapsEqual(data, want) {
		t.Fatalf("UnexpectedReturnReceived data = %v, want %v", data, want)
	}
}

func TestDispositionPayloads(t *testing.T) {
	name, data := dispositionData(t, decided())
	if name != "DispositionDecided" {
		t.Fatalf("type = %q", name)
	}
	want := map[string]any{
		"disposition_id": "disp-1", "receipt_id": "rcpt-1", "line_no": float64(1),
		"sku": "SKU-BOOK-0001", "quantity": float64(2), "grade": "A", "outcome": "RESTOCK",
		"policy_version": float64(4), "supersedes": nil, "reason": nil,
		"decided_at": "2026-10-21T10:06:00Z",
	}
	if !mapsEqual(data, want) {
		t.Fatalf("DispositionDecided data = %v, want %v", data, want)
	}

	// A policy decision carries supersedes/reason as explicit nulls, never "".
	if v, has := data["supersedes"]; !has || v != nil {
		t.Fatalf("supersedes = %v (present=%v), want explicit null", v, has)
	}
	if v, has := data["reason"]; !has || v != nil {
		t.Fatalf("reason = %v (present=%v), want explicit null", v, has)
	}

	// An override carries both.
	_, data = dispositionData(t, decidedOverride())
	if data["supersedes"] != "disp-1" || data["reason"] != "liquidation contract" {
		t.Fatalf("override data = %v", data)
	}

	name, data = dispositionData(t, staged())
	if name != "ReturnLineStaged" {
		t.Fatalf("type = %q", name)
	}
	want = map[string]any{
		"receipt_id": "rcpt-1", "line_no": float64(1), "sku": "SKU-BOOK-0001",
		"quantity": float64(2), "condition": "Good", "received_at": "2026-10-21T10:00:00Z",
		"rma_id": "rma-1", "disposition_id": "disp-1", "site_id": "site-1",
	}
	if !mapsEqual(data, want) {
		t.Fatalf("ReturnLineStaged data = %v, want %v", data, want)
	}
}

// An event the service does not publish is a programming error: the payload
// build fails and nothing is written.
func TestUnknownEventsFail(t *testing.T) {
	if _, err := outbox.RMAPayload(bogusRMA{}); err == nil {
		t.Fatal("want an error for an unknown rma event")
	}
	if _, err := outbox.ReceiptPayload(bogusReceipt{}); err == nil {
		t.Fatal("want an error for an unknown receipt event")
	}
	if _, err := outbox.DispositionPayload(bogusDisposition{}); err == nil {
		t.Fatal("want an error for an unknown disposition event")
	}
}

type bogusRMA struct{ returnauthorization.Header }

func (bogusRMA) EventName() string { return "Bogus" }

type bogusReceipt struct{ returnreceipt.Header }

func (bogusReceipt) EventName() string { return "Bogus" }

type bogusDisposition struct{ disposition.Header }

func (bogusDisposition) EventName() string { return "Bogus" }

func mapsEqual(got, want map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		g, has := got[k]
		if !has {
			return false
		}
		switch w := v.(type) {
		case []any:
			gs, ok := g.([]any)
			if !ok || len(gs) != len(w) {
				return false
			}
			for i := range w {
				if gs[i] != w[i] {
					return false
				}
			}
		default:
			if g != v {
				return false
			}
		}
	}
	return true
}
