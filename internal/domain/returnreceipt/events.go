package returnreceipt

import (
	"time"
)

// Event is a domain fact raised by the ReturnReceipt aggregate, to be written
// to the outbox. It carries no CloudEvents or Kafka type: the outbound
// adapter maps it to the wire (ADR 0004).
type Event interface {
	EventName() string
	ReceiptID() string
	ReceiptVersion() int64
	OccurredAt() time.Time
}

// Header is embedded in every receipt event.
type Header struct {
	Receipt string
	Version int64
	At      time.Time
}

// ReceiptID returns the receipt the event is about.
func (h Header) ReceiptID() string { return h.Receipt }

// ReceiptVersion returns the aggregate version after the change.
func (h Header) ReceiptVersion() int64 { return h.Version }

// OccurredAt returns when the change happened.
func (h Header) OccurredAt() time.Time { return h.At }

// ReturnReceiptOpened is raised by Open: the parcel arrived, with every line
// matched to an RMA or explicitly unexpected.
type ReturnReceiptOpened struct {
	Header
	RMAID  string // "" when the whole parcel was unexpected
	SiteID string
	Opened time.Time
	Lines  []OpenedLine
}

// EventName returns "ReturnReceiptOpened".
func (ReturnReceiptOpened) EventName() string { return "ReturnReceiptOpened" }

// OpenedLine is one line of the opened receipt, as the event carries it.
type OpenedLine struct {
	LineNo   int32
	SKU      string
	Quantity int64
	RMAID    string // "" on an UNEXPECTED line
}

// UnexpectedReturnReceived is raised by Open, once per UNEXPECTED line: a
// line arrived with no RMA; exception-management opens a case.
type UnexpectedReturnReceived struct {
	Header
	LineNo   int32
	SKU      string
	Quantity int64
	SiteID   string
	Received time.Time
}

// EventName returns "UnexpectedReturnReceived".
func (UnexpectedReturnReceived) EventName() string { return "UnexpectedReturnReceived" }

// ReturnLineInspected is raised by Inspect: the line's grade and reason
// codes are recorded. The disposition is decided by the use case in the same
// transaction and raises its own event.
type ReturnLineInspected struct {
	Header
	LineNo      int32
	SKU         string
	Quantity    int64
	Grade       string
	ReasonCodes []string
	Inspected   time.Time
}

// EventName returns "ReturnLineInspected".
func (ReturnLineInspected) EventName() string { return "ReturnLineInspected" }
