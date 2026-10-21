// Package outbox defines the neutral types of the transactional outbox: the
// claimed row (Store side, Postgres) and the encoded message (Sink side,
// Kafka), plus the domain-event → payload mapping both outbound adapters
// share. It lives in the application layer — below the adapters, which never
// import each other — and carries no Kafka or Postgres types.
//
// The mapping moved here from the Postgres publisher so the relay-side
// encoder's golden tests drive the SAME production code that writes the rows
// (one source of truth for the payload of ADR 0004), not a test copy.
package outbox

import (
	"fmt"
	"time"
)

// Aggregate type segments of the outbox rows — the same segments the
// CloudEvents `type` is built from (ADR 0004).
const (
	AggregRMA         = "returnauthorization"
	AggregReceipt     = "returnreceipt"
	AggregDisposition = "disposition"
)

// Header is one Kafka message header of an encoded row.
type Header struct {
	Key   string
	Value string
}

// Row is one claimed, still-unpublished outbox row. The Postgres relay hands
// it to the encoder; Payload is the self-describing envelope
// {"type": <EventName>, "data": {...}} the publisher wrote, where data is
// ALREADY the pinned CloudEvents data of ADR 0004.
type Row struct {
	ID         int64
	EventID    string // the CloudEvents id, minted once at publish time
	AggregType string // AggregRMA | AggregReceipt | AggregDisposition
	AggregID   string // the aggregate instance id = CloudEvents subject = Kafka key
	Payload    []byte
	OccurredAt time.Time // the domain occurred-at
}

// Message is the exact Kafka wire form of one row: everything the sink needs,
// with nothing left to decide.
type Message struct {
	// EventID is the CloudEvents `id`, the row's persisted id: a relay retry
	// republishes identical bytes.
	EventID string
	// Topic is the destination topic; the sink's writer is topic-less and
	// routes each message by this field.
	Topic string
	// EventType is the FULL CloudEvents `type`
	// (com.warehouse.wms.returns-processing.<entity>.<EventName>).
	EventType string
	// Subject is the CloudEvents `subject`.
	Subject string
	// Key is the Kafka message key, so one aggregate's events stay ordered
	// on one partition.
	Key []byte
	// DataSchema is the CloudEvents `dataschema` URN.
	DataSchema string
	// Value is the structured-mode CloudEvents JSON, ready to write.
	Value []byte
	// Headers are the Kafka headers (always includes content-type).
	Headers []Header
}

// SendFailure is the error the relay returns when the send failed for one
// row: the pass stopped there, the failure was recorded on the row and
// Attempts is the row's failure count AFTER this one (0 when recording it
// failed too). The relay logs the row that reaches the attempt cap
// (OUTBOX_MAX_ATTEMPTS) with its id, topic, type and key.
type SendFailure struct {
	RowID    int64
	Message  Message
	Attempts int
	Err      error
}

// Error keeps the "outbox row <id> (<type>): <cause>" wording the runbook and
// the alert queries grep for.
func (f *SendFailure) Error() string {
	return fmt.Sprintf("outbox row %d (%s): %v", f.RowID, f.Message.EventType, f.Err)
}

// Unwrap exposes the send error.
func (f *SendFailure) Unwrap() error { return f.Err }
