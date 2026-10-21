// Package kafka encoder: the relay side of the transactional outbox's wire
// form. The Postgres publisher persists each domain fact as a self-describing
// payload row (internal/application/outbox — the ONE payload mapping); this
// encoder turns a claimed row into the exact CloudEvents 1.0 message of
// ADR 0004 / .claude/rules/integration-events.md, or into a dead-letter
// message when the row can never become one.
package kafka

import (
	"encoding/json"
	"fmt"

	"github.com/claudioed/returns-processing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/returns-processing/internal/application/outbox"
)

// The two topics this service writes. The relay's Kafka writer carries no
// Topic and routes every message by Message.Topic (the topic-not-set-twice
// rule), so the DLQ write needs no writer of its own.
const (
	// TopicEvents is the integration topic every published type goes to.
	TopicEvents = "warehouse.returns-processing.events"
	// TopicDLQ is the dead-letter topic for outbox rows that can never be
	// encoded (fleet pattern warehouse.<ctx>.events.dlq).
	TopicDLQ = "warehouse.returns-processing.events.dlq"
)

// schemaVersion is the dataschema version of every payload
// (urn:warehouse:returns-processing:events:<EventName>:v1).
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
// EventsTopic/DLQTopic so each run owns its own (explicitly created) topics.
type Encoder struct {
	EventsTopic string
	DLQTopic    string
}

// NewEncoder returns an Encoder on the fleet topics.
func NewEncoder() *Encoder { return &Encoder{EventsTopic: TopicEvents, DLQTopic: TopicDLQ} }

// claimed is the parsed payload of one row.
type claimed struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Encode maps row to its exact CloudEvents message: the full `type` built
// from the row's aggregate segment and the payload's event name, `source`
// /warehouse/returns-processing, `subject` and Kafka key the aggregate id,
// `time` the domain occurred-at UTC, `dataschema`
// urn:warehouse:returns-processing:events:<EventName>:v1 and the structured
// JSON value with the content-type header.
//
// It NEVER returns an error. ok=false marks a poison row — a payload that is
// not valid JSON, an event the service does not publish, or a row the
// envelope rejects (e.g. an empty aggregate id) — together with a
// human-readable cause: such a row is dead-lettered, never retried, because
// no retry can change what it is.
func (e *Encoder) Encode(row outbox.Row) (msg outbox.Message, cause string, ok bool) {
	var env claimed
	if err := json.Unmarshal(row.Payload, &env); err != nil {
		return outbox.Message{}, fmt.Sprintf("malformed outbox payload: %v", err), false
	}
	if !published[row.AggregType][env.Type] {
		return outbox.Message{}, fmt.Sprintf("%s.%s is not a published event of ADR 0004", row.AggregType, env.Type), false
	}
	value, err := cloudevents.New(cloudevents.Spec{
		ID:        row.EventID,
		Entity:    row.AggregType,
		EventName: env.Type,
		Subject:   row.AggregID,
		Time:      row.OccurredAt,
		Stream:    cloudevents.StreamEvents,
		Version:   schemaVersion,
		Data:      env.Data,
	})
	if err != nil {
		return outbox.Message{}, fmt.Sprintf("unencodable %s.%s row: %v", row.AggregType, env.Type, err), false
	}
	ct := cloudevents.ContentTypeHeader()
	return outbox.Message{
		EventID:    row.EventID,
		Topic:      e.EventsTopic,
		EventType:  cloudevents.Type(row.AggregType, env.Type),
		Subject:    row.AggregID,
		Key:        []byte(row.AggregID),
		DataSchema: cloudevents.DataSchema(cloudevents.StreamEvents, env.Type, schemaVersion),
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
