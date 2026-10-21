package disposition

import (
	"time"
)

// Event is a domain fact raised by the Disposition aggregate, to be written
// to the outbox. It carries no CloudEvents or Kafka type: the outbound
// adapter maps it to the wire (ADR 0004).
type Event interface {
	EventName() string
	DispositionID() string
	DispositionVersion() int64
	OccurredAt() time.Time
}

// Header is embedded in every disposition event.
type Header struct {
	Disposition string
	Version     int64
	At          time.Time
}

// DispositionID returns the disposition the event is about.
func (h Header) DispositionID() string { return h.Disposition }

// DispositionVersion returns the aggregate version after the change.
func (h Header) DispositionVersion() int64 { return h.Version }

// OccurredAt returns when the change happened.
func (h Header) OccurredAt() time.Time { return h.At }

// DispositionDecided is raised by every decision, policy or override.
type DispositionDecided struct {
	Header
	ReceiptID     string
	LineNo        int32
	SKU           string
	Quantity      int64
	Grade         string
	Outcome       Outcome
	PolicyVersion int64
	// Supersedes is "" on a policy decision; the superseded disposition id on
	// an override.
	Supersedes string
	// Reason is "" on a policy decision; the mandatory override reason.
	Reason    string
	DecidedAt time.Time
}

// EventName returns "DispositionDecided".
func (DispositionDecided) EventName() string { return "DispositionDecided" }

// ReturnLineStaged is raised exactly once per RESTOCK disposition: the
// handover of the goods to inventory-storage, in inbound-receiving's
// ReceiptLineReceived shape plus rma_id, disposition_id and site_id. The
// DispositionID is the idempotency key downstream.
type ReturnLineStaged struct {
	Header
	ReceiptID  string
	LineNo     int32
	SKU        string
	Quantity   int64
	Condition  string // always "Good" (the ReceiptLineReceived shape)
	ReceivedAt time.Time
	RMAID      string
	SiteID     string
}

// EventName returns "ReturnLineStaged".
func (ReturnLineStaged) EventName() string { return "ReturnLineStaged" }

// StagedCondition is the one condition a staged return line carries.
const StagedCondition = "Good"
