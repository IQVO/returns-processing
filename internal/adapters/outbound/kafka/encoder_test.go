package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/returns-processing/internal/adapters/kafka/cloudevents"
	outboundkafka "github.com/claudioed/returns-processing/internal/adapters/outbound/kafka"
	"github.com/claudioed/returns-processing/internal/application/outbox"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// One golden case per published type (ADR 0004): the payload is built by the
// SAME production mapping the Postgres publisher uses, so these tests pin the
// full chain domain fact -> outbox payload -> exact CloudEvents JSON,
// including the Kafka key and the content-type header.

func at(h, m int) time.Time { return time.Date(2026, 10, 21, h, m, 0, 0, time.UTC) }

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

type goldenCase struct {
	name      string
	id        string
	aggregID  string
	occurred  time.Time
	eventType string
	schema    string
	golden    string
	row       func(t *testing.T) outbox.Row
}

func rowOf(t *testing.T, aggregType, eventID, aggregID string, occurred time.Time, payload []byte, err error) outbox.Row {
	t.Helper()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	return outbox.Row{ID: 1, EventID: eventID, AggregType: aggregType, AggregID: aggregID, Payload: payload, OccurredAt: occurred}
}

func goldenCases() []goldenCase {
	return []goldenCase{
		{
			name: "ReturnAuthorized", id: "9a7d2e64-1b3f-4c8a-9d5e-2f6b8a4c1e70", aggregID: "rma-1", occurred: at(9, 30),
			eventType: "com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorized",
			schema:    "urn:warehouse:returns-processing:events:ReturnAuthorized:v1",
			golden:    "return_authorized.golden.json",
			row: func(t *testing.T) outbox.Row {
				e := returnauthorization.ReturnAuthorized{
					Header:   returnauthorization.Header{RMA: "rma-1", Version: 2, At: at(9, 30)},
					OrderRef: orderLine(t, "ord-7c9e", 3),
					SKU:      "SKU-BOOK-0001", Quantity: 2, Site: "site-1", ReasonCode: "DAMAGED",
					WindowEnds: at(11, 30), Authorized: at(9, 30),
				}
				b, err := outbox.RMAPayload(e)
				return rowOf(t, outbox.AggregRMA, "9a7d2e64-1b3f-4c8a-9d5e-2f6b8a4c1e70", "rma-1", at(9, 30), b, err)
			},
		},
		{
			name: "ReturnAuthorizationRejected", id: "0f5c3a92-7d41-4b6e-8c2f-1a9b8d3e5f42", aggregID: "rma-2", occurred: at(9, 31),
			eventType: "com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorizationRejected",
			schema:    "urn:warehouse:returns-processing:events:ReturnAuthorizationRejected:v1",
			golden:    "return_authorization_rejected.golden.json",
			row: func(t *testing.T) outbox.Row {
				e := returnauthorization.ReturnAuthorizationRejected{
					Header:   returnauthorization.Header{RMA: "rma-2", Version: 2, At: at(9, 31)},
					OrderRef: orderLine(t, "ord-7c9e", 4),
					SKU:      "SKU-9", Quantity: 5,
					RejectionCode: returnauthorization.RejectLineNotManifested, RejectedAt: at(9, 31),
				}
				b, err := outbox.RMAPayload(e)
				return rowOf(t, outbox.AggregRMA, "0f5c3a92-7d41-4b6e-8c2f-1a9b8d3e5f42", "rma-2", at(9, 31), b, err)
			},
		},
		{
			name: "ReturnReceiptOpened", id: "3e8b1c5d-9f4a-4d7e-b3c2-6a1e8f0d9b53", aggregID: "rcpt-1", occurred: at(10, 0),
			eventType: "com.warehouse.wms.returns-processing.returnreceipt.ReturnReceiptOpened",
			schema:    "urn:warehouse:returns-processing:events:ReturnReceiptOpened:v1",
			golden:    "return_receipt_opened.golden.json",
			row: func(t *testing.T) outbox.Row {
				e := returnreceipt.ReturnReceiptOpened{
					Header: returnreceipt.Header{Receipt: "rcpt-1", Version: 2, At: at(10, 0)},
					RMAID:  "rma-1", SiteID: "site-1", Opened: at(10, 0),
					Lines: []returnreceipt.OpenedLine{
						{LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2, RMAID: "rma-1"},
						{LineNo: 2, SKU: "SKU-X", Quantity: 1},
					},
				}
				b, err := outbox.ReceiptPayload(e)
				return rowOf(t, outbox.AggregReceipt, "3e8b1c5d-9f4a-4d7e-b3c2-6a1e8f0d9b53", "rcpt-1", at(10, 0), b, err)
			},
		},
		{
			name: "ReturnLineInspected", id: "5d2f7a8e-3c6b-4e19-a7d4-8b0c2e5f1a66", aggregID: "rcpt-1", occurred: at(10, 5),
			eventType: "com.warehouse.wms.returns-processing.returnreceipt.ReturnLineInspected",
			schema:    "urn:warehouse:returns-processing:events:ReturnLineInspected:v1",
			golden:    "return_line_inspected.golden.json",
			row: func(t *testing.T) outbox.Row {
				e := returnreceipt.ReturnLineInspected{
					Header: returnreceipt.Header{Receipt: "rcpt-1", Version: 3, At: at(10, 5)},
					LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2, Grade: "A",
					ReasonCodes: []string{"like-new"}, Inspected: at(10, 5),
				}
				b, err := outbox.ReceiptPayload(e)
				return rowOf(t, outbox.AggregReceipt, "5d2f7a8e-3c6b-4e19-a7d4-8b0c2e5f1a66", "rcpt-1", at(10, 5), b, err)
			},
		},
		{
			name: "UnexpectedReturnReceived", id: "7c9e4b1a-5d38-4f2c-b6e9-0a4d7c3f8e21", aggregID: "rcpt-1", occurred: at(10, 0),
			eventType: "com.warehouse.wms.returns-processing.returnreceipt.UnexpectedReturnReceived",
			schema:    "urn:warehouse:returns-processing:events:UnexpectedReturnReceived:v1",
			golden:    "unexpected_return_received.golden.json",
			row: func(t *testing.T) outbox.Row {
				e := returnreceipt.UnexpectedReturnReceived{
					Header: returnreceipt.Header{Receipt: "rcpt-1", Version: 2, At: at(10, 0)},
					LineNo: 2, SKU: "SKU-X", Quantity: 1, SiteID: "site-1", Received: at(10, 0),
				}
				b, err := outbox.ReceiptPayload(e)
				return rowOf(t, outbox.AggregReceipt, "7c9e4b1a-5d38-4f2c-b6e9-0a4d7c3f8e21", "rcpt-1", at(10, 0), b, err)
			},
		},
		{
			name: "DispositionDecided", id: "1b6a9d3c-8e47-4c05-b2f8-5d1e9a7c4b02", aggregID: "disp-1", occurred: at(10, 6),
			eventType: "com.warehouse.wms.returns-processing.disposition.DispositionDecided",
			schema:    "urn:warehouse:returns-processing:events:DispositionDecided:v1",
			golden:    "disposition_decided.golden.json",
			row: func(t *testing.T) outbox.Row {
				e := disposition.DispositionDecided{
					Header:    disposition.Header{Disposition: "disp-1", Version: 1, At: at(10, 6)},
					ReceiptID: "rcpt-1", LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2, Grade: "A",
					Outcome: disposition.OutcomeRestock, PolicyVersion: 4, DecidedAt: at(10, 6),
				}
				b, err := outbox.DispositionPayload(e)
				return rowOf(t, outbox.AggregDisposition, "1b6a9d3c-8e47-4c05-b2f8-5d1e9a7c4b02", "disp-1", at(10, 6), b, err)
			},
		},
		{
			name: "ReturnLineStaged", id: "4e7c2f9b-6a13-4d58-9c8e-3b5f1a2d7e94", aggregID: "disp-1", occurred: at(10, 6),
			eventType: "com.warehouse.wms.returns-processing.disposition.ReturnLineStaged",
			schema:    "urn:warehouse:returns-processing:events:ReturnLineStaged:v1",
			golden:    "return_line_staged.golden.json",
			row: func(t *testing.T) outbox.Row {
				e := disposition.ReturnLineStaged{
					Header:    disposition.Header{Disposition: "disp-1", Version: 2, At: at(10, 6)},
					ReceiptID: "rcpt-1", LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2,
					Condition: disposition.StagedCondition, ReceivedAt: at(10, 0),
					RMAID: "rma-1", SiteID: "site-1",
				}
				b, err := outbox.DispositionPayload(e)
				return rowOf(t, outbox.AggregDisposition, "4e7c2f9b-6a13-4d58-9c8e-3b5f1a2d7e94", "disp-1", at(10, 6), b, err)
			},
		},
	}
}

func decodeJSON(t *testing.T, what string, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", what, err, raw)
	}
	return v
}

func readGolden(t *testing.T, name string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return decodeJSON(t, name, raw)
}

// assertGoldenMessage checks one encoded message against its golden case:
// every routing column, the Kafka key, exactly the content-type header, a
// CloudEvents-valid value and the whole envelope against the golden file.
func assertGoldenMessage(t *testing.T, c goldenCase, msg outbox.Message) {
	t.Helper()
	if msg.Topic != outboundkafka.TopicEvents {
		t.Errorf("topic = %q, want %q", msg.Topic, outboundkafka.TopicEvents)
	}
	if msg.EventID != c.id || msg.EventType != c.eventType || msg.Subject != c.aggregID || msg.DataSchema != c.schema {
		t.Errorf("msg = id %q type %q subject %q schema %q", msg.EventID, msg.EventType, msg.Subject, msg.DataSchema)
	}
	if string(msg.Key) != c.aggregID {
		t.Errorf("key = %q, want %q", msg.Key, c.aggregID)
	}
	wantHeaders := []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}}
	if !reflect.DeepEqual(msg.Headers, wantHeaders) {
		t.Errorf("headers = %+v, want %+v", msg.Headers, wantHeaders)
	}
	if _, err := cloudevents.Decode(msg.Value); err != nil {
		t.Fatalf("value is not a valid CloudEvent: %v", err)
	}
	if got, want := decodeJSON(t, "value", msg.Value), readGolden(t, c.golden); !reflect.DeepEqual(got, want) {
		t.Errorf("%s: value differs from the golden\n got: %s\nwant: %v", c.golden, msg.Value, want)
	}
}

// TestEncoder_GoldenJSONPerType pins, per published type, every CloudEvents
// attribute, the whole value against the golden file, the Kafka key and
// exactly the content-type header.
func TestEncoder_GoldenJSONPerType(t *testing.T) {
	for _, c := range goldenCases() {
		t.Run(c.name, func(t *testing.T) {
			msg, cause, ok := outboundkafka.NewEncoder().Encode(c.row(t))
			if !ok {
				t.Fatalf("encode failed: %s", cause)
			}
			assertGoldenMessage(t, c, msg)
		})
	}
}

// Every encoded value decodes with the repo's own validator and carries the
// service source and the application/json data type.
func TestEncoder_ValuesDecodeAsCloudEvents(t *testing.T) {
	enc := outboundkafka.NewEncoder()
	for _, c := range goldenCases() {
		msg, _, ok := enc.Encode(c.row(t))
		if !ok {
			t.Fatalf("%s: encode failed", c.name)
		}
		e, err := cloudevents.Decode(msg.Value)
		if err != nil {
			t.Fatalf("%s: decode: %v", c.name, err)
		}
		if e.Source() != "/warehouse/returns-processing" || e.DataContentType() != "application/json" || e.ID() != c.id {
			t.Errorf("%s: source %q datacontenttype %q id %q", c.name, e.Source(), e.DataContentType(), e.ID())
		}
	}
}

// Unknown domain events yield NOTHING, never an error: a payload that is not
// valid JSON, an unpublished event name, an unknown aggregate segment and a
// row the envelope rejects (empty subject) are all poison, each with a cause.
func TestEncoder_UnknownYieldsNothing(t *testing.T) {
	enc := outboundkafka.NewEncoder()
	authorized := goldenCases()[0].row(t)

	cases := []struct {
		name   string
		modify func(r outbox.Row) outbox.Row
	}{
		{"unknown event name", func(r outbox.Row) outbox.Row {
			r.Payload = []byte(`{"type":"Bogus","data":{}}`)
			return r
		}},
		{"known name, wrong aggregate", func(r outbox.Row) outbox.Row {
			r.AggregType = "disposition"
			return r
		}},
		{"unknown aggregate segment", func(r outbox.Row) outbox.Row {
			r.AggregType = "somethingelse"
			return r
		}},
		{"malformed payload JSON", func(r outbox.Row) outbox.Row {
			r.Payload = []byte(`{"type":"ReturnAuthorized","data":`)
			return r
		}},
		{"payload is not an envelope", func(r outbox.Row) outbox.Row {
			r.Payload = []byte(`42`)
			return r
		}},
		{"empty aggregate id", func(r outbox.Row) outbox.Row {
			r.AggregID = ""
			return r
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, cause, ok := enc.Encode(tc.modify(authorized))
			if ok {
				t.Fatalf("Encode = (%+v), want not-ok", msg)
			}
			if msg.Topic != "" || msg.Value != nil {
				t.Errorf("poison message = %+v, want the zero message", msg)
			}
			if cause == "" {
				t.Error("cause is empty, want a human-readable reason")
			}
		})
	}
}

// The dead-letter message carries the row's payload VERBATIM, the aggregate id
// as key, an application/json content type and the cause in dlq-reason.
func TestEncoder_DeadLetter(t *testing.T) {
	enc := outboundkafka.NewEncoder()
	row := goldenCases()[0].row(t)
	row.AggregType = "returnauthorization"
	row.Payload = []byte(`{"type":"Bogus","data":{}}`)

	_, cause, ok := enc.Encode(row)
	if ok {
		t.Fatal("row unexpectedly encodable")
	}
	dlq := enc.DeadLetter(row, cause)
	if dlq.Topic != outboundkafka.TopicDLQ {
		t.Errorf("topic = %q, want %q", dlq.Topic, outboundkafka.TopicDLQ)
	}
	if dlq.EventID != row.EventID || dlq.Subject != row.AggregID || string(dlq.Key) != row.AggregID {
		t.Errorf("dlq = id %q subject %q key %q", dlq.EventID, dlq.Subject, dlq.Key)
	}
	if string(dlq.Value) != string(row.Payload) {
		t.Errorf("value = %s, want the payload verbatim", dlq.Value)
	}
	want := []outbox.Header{
		{Key: "content-type", Value: "application/json"},
		{Key: "dlq-reason", Value: cause},
	}
	if !reflect.DeepEqual(dlq.Headers, want) {
		t.Errorf("headers = %+v, want %+v", dlq.Headers, want)
	}
}

// A huge cause is bounded, so a poison payload error never floods the headers.
func TestEncoder_DeadLetterBoundsCause(t *testing.T) {
	enc := outboundkafka.NewEncoder()
	row := goldenCases()[0].row(t)
	dlq := enc.DeadLetter(row, string(make([]byte, 4096)))
	for _, h := range dlq.Headers {
		if h.Key == "dlq-reason" && len(h.Value) > 512 {
			t.Fatalf("dlq-reason is %d bytes, want <= 512", len(h.Value))
		}
	}
}

func TestEncoder_Topics(t *testing.T) {
	enc := outboundkafka.NewEncoder()
	if enc.EventsTopic != "warehouse.returns-processing.events" || enc.DLQTopic != "warehouse.returns-processing.events.dlq" {
		t.Fatalf("topics = %q, %q", enc.EventsTopic, enc.DLQTopic)
	}
	enc.EventsTopic, enc.DLQTopic = "t.events", "t.dlq"
	msg, _, ok := enc.Encode(goldenCases()[0].row(t))
	if !ok || msg.Topic != "t.events" {
		t.Fatalf("msg.Topic = %q ok=%v, want the override", msg.Topic, ok)
	}
	if dlq := enc.DeadLetter(goldenCases()[0].row(t), "x"); dlq.Topic != "t.dlq" {
		t.Fatalf("dlq.Topic = %q, want the override", dlq.Topic)
	}
}

// fakeWriter records everything written through it.
type fakeWriter struct {
	written [][]kafkago.Message
	err     error
}

func (f *fakeWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	if f.err != nil {
		return f.err
	}
	f.written = append(f.written, msgs)
	return nil
}

func TestRelaySink_SendsEncodedMessagesWithTopicKeyHeaders(t *testing.T) {
	w := &fakeWriter{}
	sink := outboundkafka.NewRelaySinkWithWriter(w)
	msg, _, ok := outboundkafka.NewEncoder().Encode(goldenCases()[0].row(t))
	if !ok {
		t.Fatal("encode failed")
	}
	if err := sink.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(w.written) != 1 || len(w.written[0]) != 1 {
		t.Fatalf("writes = %+v, want one write of one message", w.written)
	}
	got := w.written[0][0]
	if got.Topic != outboundkafka.TopicEvents || string(got.Key) != "rma-1" || string(got.Value) != string(msg.Value) {
		t.Errorf("message = topic %q key %q value %s", got.Topic, got.Key, got.Value)
	}
	if len(got.Headers) != 1 || got.Headers[0].Key != "content-type" || string(got.Headers[0].Value) != cloudevents.MediaType {
		t.Errorf("headers = %+v", got.Headers)
	}
}

func TestRelaySink_RejectsTopiclessMessages(t *testing.T) {
	w := &fakeWriter{}
	sink := outboundkafka.NewRelaySinkWithWriter(w)
	if err := sink.Send(context.Background(), outbox.Message{EventType: "X"}); err == nil {
		t.Fatal("want an error for a message without a topic")
	}
	if len(w.written) != 0 {
		t.Fatalf("wrote %+v despite the rejection", w.written)
	}
}

func TestRelaySink_EmptyListIsANoop(t *testing.T) {
	w := &fakeWriter{}
	if err := outboundkafka.NewRelaySinkWithWriter(w).Send(context.Background()); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(w.written) != 0 {
		t.Fatalf("wrote %+v, want nothing", w.written)
	}
}

func TestRelaySink_WrapsWriterErrors(t *testing.T) {
	boom := errors.New("broker unreachable")
	sink := outboundkafka.NewRelaySinkWithWriter(&fakeWriter{err: boom})
	msg, _, _ := outboundkafka.NewEncoder().Encode(goldenCases()[0].row(t))
	err := sink.Send(context.Background(), msg)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap boom", err)
	}
}
