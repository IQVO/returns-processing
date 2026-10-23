// Package kafka encoder: the relay side of the transactional outbox's wire
// form. The Postgres publisher persists each domain fact as a self-describing
// payload row (internal/application/outbox — the ONE payload mapping); this
// encoder turns a claimed row into the exact CloudEvents 1.0 messages of
// ADR 0004 / .claude/rules/integration-events.md — one per destination topic:
// the integration topic and its analytics twin (ADR 0007) — or into a
// dead-letter message when the row can never become one.
package kafka

import (
	"encoding/json"
	"fmt"

	"github.com/claudioed/returns-processing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/returns-processing/internal/application/outbox"
)

// The topics this service writes. The relay's Kafka writer carries no
// Topic and routes every message by Message.Topic (the topic-not-set-twice
// rule), so the DLQ write needs no writer of its own.
const (
	// TopicEvents is the integration topic every published type goes to.
	TopicEvents = "warehouse.returns-processing.events"
	// TopicAnalytics is the analytics topic (ADR 0007): EVERY published type
	// goes there too, under the same CloudEvents id and type and with a
	// byte-identical payload; only the dataschema stream segment differs
	// (urn:warehouse:returns-processing:analytics:<EventName>:v1). Its only
	// consumer is this service's cmd/returns-projector.
	TopicAnalytics = "warehouse.returns-processing.analytics"
	// TopicDLQ is the dead-letter topic for outbox rows that can never be
	// encoded (fleet pattern warehouse.<ctx>.events.dlq).
	TopicDLQ = "warehouse.returns-processing.events.dlq"
)

// schemaVersion is the dataschema version of every payload
// (urn:warehouse:returns-processing:<events|analytics>:<EventName>:v1).
const schemaVersion = 1

// published is the catalogue of ADR 0004: aggregate segment -> the event
// names the segment publishes. A row outside it is poison: deterministic,
// never retryable, dead-lettered.
var published = map[string]map[string]bool{
	outbox.AggregRMA:         {"ReturnAuthorized": true, "ReturnAuthorizationRejected": true},
	outbox.AggregReceipt:     {"ReturnReceiptOpened": true, "ReturnLineInspected": true, "UnexpectedReturnReceived": true},
	outbox.AggregDisposition: {"DispositionDecided": true, "ReturnLineStaged": true},
}

// maxCause bounds a dead-letter reason: Kafka headers are for operators, not
// for payloads.
const maxCause = 512

// Encoder maps claimed outbox rows to their Kafka wire form. The zero value
// is not usable; NewEncoder sets the fleet topics, and a test overrides
// EventsTopic/AnalyticsTopic/DLQTopic so each run owns its own (explicitly
// created) topics.
type Encoder struct {
	EventsTopic    string
	AnalyticsTopic string
	DLQTopic       string
}

// NewEncoder returns an Encoder on the fleet topics.
func NewEncoder() *Encoder {
	return &Encoder{EventsTopic: TopicEvents, AnalyticsTopic: TopicAnalytics, DLQTopic: TopicDLQ}
}

func (e *Encoder) eventsTopic() string {
	if e.EventsTopic == "" {
		return TopicEvents
	}
	return e.EventsTopic
}

func (e *Encoder) analyticsTopic() string {
	if e.AnalyticsTopic == "" {
		return TopicAnalytics
	}
	return e.AnalyticsTopic
}

// claimed is the parsed payload of one row.
type claimed struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// destination is one topic a row is published to, with the CloudEvents
// dataschema stream segment that names it.
type destination struct {
	topic  string
	stream string
}

// destinations are the two streams every published occurrence goes to
// (ADR 0007): the integration contract and this service's own analytics
// stream. The payload is byte-identical on both; only the dataschema differs.
func (e *Encoder) destinations() []destination {
	return []destination{
		{topic: e.eventsTopic(), stream: cloudevents.StreamEvents},
		{topic: e.analyticsTopic(), stream: cloudevents.StreamAnalytics},
	}
}

// Encode maps row to its exact CloudEvents messages: one per destination
// topic. Every message carries the full `type` built from the row's aggregate
// segment and the payload's event name, `source` /warehouse/returns-processing,
// `subject` and Kafka key the aggregate id, `time` the domain occurred-at UTC,
// the same CloudEvents `id` (a relay retry republishes the same bytes on both
// topics, and the analytics consumer dedupes on it), `dataschema`
// urn:warehouse:returns-processing:<events|analytics>:<EventName>:v1 and the
// structured JSON value with the content-type header.
//
// It NEVER returns an error. ok=false marks a poison row — a payload that is
// not valid JSON, an event the service does not publish, or a row the
// envelope rejects (e.g. an empty aggregate id) — together with a
// human-readable cause: such a row is dead-lettered, never retried, because
// no retry can change what it is.
func (e *Encoder) Encode(row outbox.Row) (msgs []outbox.Message, cause string, ok bool) {
	var env claimed
	if err := json.Unmarshal(row.Payload, &env); err != nil {
		return nil, fmt.Sprintf("malformed outbox payload: %v", err), false
	}
	if !published[row.AggregType][env.Type] {
		return nil, fmt.Sprintf("%s.%s is not a published event of ADR 0004", row.AggregType, env.Type), false
	}
	out := make([]outbox.Message, 0, len(e.destinations()))
	for _, d := range e.destinations() {
		msg, cause, ok := e.message(row, env, d)
		if !ok {
			return nil, cause, false
		}
		out = append(out, msg)
	}
	return out, "", true
}

// message builds the wire form of row for one destination.
func (e *Encoder) message(row outbox.Row, env claimed, d destination) (outbox.Message, string, bool) {
	value, err := cloudevents.New(cloudevents.Spec{
		ID:        row.EventID,
		Entity:    row.AggregType,
		EventName: env.Type,
		Subject:   row.AggregID,
		Time:      row.OccurredAt,
		Stream:    d.stream,
		Version:   schemaVersion,
		Data:      env.Data,
	})
	if err != nil {
		return outbox.Message{}, fmt.Sprintf("unencodable %s.%s row: %v", row.AggregType, env.Type, err), false
	}
	ct := cloudevents.ContentTypeHeader()
	return outbox.Message{
		EventID:    row.EventID,
		Topic:      d.topic,
		EventType:  cloudevents.Type(row.AggregType, env.Type),
		Subject:    row.AggregID,
		Key:        []byte(row.AggregID),
		DataSchema: cloudevents.DataSchema(d.stream, env.Type, schemaVersion),
		Value:      value,
		Headers:    []outbox.Header{{Key: ct.Key, Value: string(ct.Value)}},
	}, "", true
}

// DeadLetter builds the dead-letter message of a poison row: the row's
// payload VERBATIM as the value (an operator sees exactly what failed and a
// redrive tool can replay it), the aggregate id as the key, an application/json
// content-type header and the cause in dlq-reason.
func (e *Encoder) DeadLetter(row outbox.Row, cause string) outbox.Message {
	if len(cause) > maxCause {
		cause = cause[:maxCause]
	}
	return outbox.Message{
		EventID: row.EventID,
		Topic:   e.DLQTopic,
		Subject: row.AggregID,
		Key:     []byte(row.AggregID),
		Value:   row.Payload,
		Headers: []outbox.Header{{Key: "content-type", Value: "application/json"}, {Key: "dlq-reason", Value: cause}},
	}
}
