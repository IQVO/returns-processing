package kafka_test

import (
	"encoding/json"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
)

// envelope builds a valid CloudEvents 1.0 structured-mode JSON of a CONSUMED
// type: the producer's source and type strings, not this service's — the
// consumers decode what order-management, product-master and
// fulfillment-execution publish.
func envelope(t *testing.T, typ, source, subject, id string, data any) []byte {
	t.Helper()
	e := ce.New(ce.CloudEventsVersionV1)
	e.SetID(id)
	e.SetSource(source)
	e.SetType(typ)
	e.SetSubject(subject)
	e.SetTime(time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC))
	e.SetDataSchema("urn:" + source + ":" + typ + ":v1")
	if err := e.SetData("application/json", data); err != nil {
		t.Fatalf("set data: %v", err)
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// legacyFlatEnvelope is the retired flat envelope (event_id/event_type at the
// TOP level): a consumer must reject it, never fall back to parsing it.
func legacyFlatEnvelope() []byte {
	return []byte(`{"event_id":"e-1","event_type":"com.warehouse.wms.product-master.product.ProductClassified","occurred_at":"2026-10-21T09:30:00Z","data":{"sku":"SKU-1"}}`)
}
