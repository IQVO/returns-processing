package returnauthorization

import (
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// Event is a domain fact raised by the RMA aggregate, to be written to the
// outbox. It carries no CloudEvents or Kafka type: the outbound adapter maps
// it to the wire (ADR 0004).
type Event interface {
	EventName() string
	RMAID() string
	RMAVersion() int64
	OccurredAt() time.Time
}

// Header is embedded in every RMA event.
type Header struct {
	RMA     string
	Version int64
	At      time.Time
}

// RMAID returns the rma the event is about.
func (h Header) RMAID() string { return h.RMA }

// RMAVersion returns the aggregate version after the change.
func (h Header) RMAVersion() int64 { return h.Version }

// OccurredAt returns when the change happened.
func (h Header) OccurredAt() time.Time { return h.At }

// ReturnAuthorized is raised by Authorize: quantity units of SKU may come
// back to the site until WindowEndsAt.
type ReturnAuthorized struct {
	Header
	OrderRef   shared.OrderLineRef
	SKU        string
	Quantity   int64
	Site       string
	ReasonCode string
	WindowEnds time.Time
	Authorized time.Time
}

// EventName returns "ReturnAuthorized".
func (ReturnAuthorized) EventName() string { return "ReturnAuthorized" }

// ReturnAuthorizationRejected is raised by Reject with the refusal code.
type ReturnAuthorizationRejected struct {
	Header
	OrderRef      shared.OrderLineRef
	SKU           string
	Quantity      int64
	RejectionCode RejectionCode
	RejectedAt    time.Time
}

// EventName returns "ReturnAuthorizationRejected".
func (ReturnAuthorizationRejected) EventName() string { return "ReturnAuthorizationRejected" }
