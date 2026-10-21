// Package returnauthorization holds the ReturnAuthorization (RMA) aggregate
// (ADR 0002): permission to send one shipped order line back. It is pure —
// no I/O, no clock reads; the authorization facts (manifested work unit,
// open sibling RMA, policy caps) are handed in from the application layer's
// event-fed local copies (ADR 0003).
package returnauthorization

import (
	"errors"
)

// Status is the lifecycle state of an RMA (ADR 0002, exactly as
// .claude/rules/domain-model.md states it): REQUESTED -> AUTHORIZED |
// REJECTED; AUTHORIZED -> RECEIVED; RECEIVED -> CLOSED. REJECTED and CLOSED
// are terminal: no transition leaves them.
type Status string

// The states of an RMA.
const (
	StatusRequested  Status = "REQUESTED"
	StatusAuthorized Status = "AUTHORIZED"
	StatusRejected   Status = "REJECTED"
	StatusReceived   Status = "RECEIVED"
	StatusClosed     Status = "CLOSED"
)

// Valid reports whether s is one of the five states.
func (s Status) Valid() bool {
	switch s {
	case StatusRequested, StatusAuthorized, StatusRejected, StatusReceived, StatusClosed:
		return true
	default:
		return false
	}
}

// RejectionCode says why an RMA request was refused. The four codes are the
// contract's enum (apis/asyncapi.yaml).
type RejectionCode string

// The rejection codes.
const (
	RejectLineNotManifested   RejectionCode = "LINE_NOT_MANIFESTED"
	RejectReturnWindowExpired RejectionCode = "RETURN_WINDOW_EXPIRED"
	RejectOpenRMAExists       RejectionCode = "OPEN_RMA_EXISTS"
	RejectQuantityExceedsMax  RejectionCode = "QUANTITY_EXCEEDS_POLICY"
)

// Valid reports whether c is one of the four codes.
func (c RejectionCode) Valid() bool {
	switch c {
	case RejectLineNotManifested, RejectReturnWindowExpired, RejectOpenRMAExists, RejectQuantityExceedsMax:
		return true
	default:
		return false
	}
}

// RejectionCodeFor maps an Authorize error to the rejection code the contract
// publishes ("" when the error is not a rejection).
func RejectionCodeFor(err error) RejectionCode {
	switch {
	case errors.Is(err, ErrLineNotManifested):
		return RejectLineNotManifested
	case errors.Is(err, ErrWindowExpired):
		return RejectReturnWindowExpired
	case errors.Is(err, ErrOpenRMAExists):
		return RejectOpenRMAExists
	case errors.Is(err, ErrQuantityExceedsPolicy):
		return RejectQuantityExceedsMax
	default:
		return ""
	}
}

// Domain errors, as sentinel values (compare with errors.Is).
var (
	// ErrBlankReasonCode: the return reason is empty or only white space.
	ErrBlankReasonCode = errors.New("returnauthorization: reason code must not be blank")
	// ErrNoTimestamp: a transition was given the zero time.
	ErrNoTimestamp = errors.New("returnauthorization: a timestamp is required")
	// ErrNoWindow: the policy window end is the zero time.
	ErrNoWindow = errors.New("returnauthorization: a return window end is required")
	// ErrInvalidStatus: a restored status is not a known status.
	ErrInvalidStatus = errors.New("returnauthorization: unknown status")
	// ErrInvalidRejectionCode: the rejection code is not one of the four.
	ErrInvalidRejectionCode = errors.New("returnauthorization: unknown rejection code")
	// ErrAlreadyDecided: the RMA is past REQUESTED, so it cannot be decided again.
	ErrAlreadyDecided = errors.New("returnauthorization: rma is already decided")
	// ErrOpenRMAExists: another RMA is already open for this order line (invariant 1).
	ErrOpenRMAExists = errors.New("returnauthorization: an rma is already open for this order line")
	// ErrLineNotManifested: no manifested work unit exists for the line (invariant 2).
	ErrLineNotManifested = errors.New("returnauthorization: no manifested work unit for this order line")
	// ErrWindowExpired: now is past the window end (invariant 2).
	ErrWindowExpired = errors.New("returnauthorization: the return window has expired")
	// ErrQuantityExceedsPolicy: the quantity passes the policy cap (invariant 3).
	ErrQuantityExceedsPolicy = errors.New("returnauthorization: quantity exceeds the policy maximum")
	// ErrNotAuthorized: only an AUTHORIZED RMA can be received.
	ErrNotAuthorized = errors.New("returnauthorization: only an authorized rma can be received")
	// ErrNotReceived: only a RECEIVED RMA can be closed.
	ErrNotReceived = errors.New("returnauthorization: only a received rma can be closed")
	// ErrTerminal: REJECTED and CLOSED have no transitions out.
	ErrTerminal = errors.New("returnauthorization: status is terminal (REJECTED or CLOSED)")
	// ErrInvalidMaxUnits: the policy cap handed in is below 1.
	ErrInvalidMaxUnits = errors.New("returnauthorization: policy max units per line must be at least 1")
	// ErrInvalidSnapshot: a persisted RMA is internally inconsistent.
	ErrInvalidSnapshot = errors.New("returnauthorization: invalid snapshot")
)
