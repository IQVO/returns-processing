package views

import "time"

// The network return-advice read model of ADR 0012: the poller-fed local
// copy of the retail network's return authorizations, keyed authorizationId,
// plus the graded outcome this service reports back. These DTOs cross the
// port boundaries in both directions: the outbound gateway produces
// ReturnAdvice rows and consumes ReturnOutcome; the store persists them.

// The network-owned enums, as strings: the application never branches on them
// (the network disposition is a recorded INPUT, never re-decided).
const (
	// AdviceStateAuthorized/CANCELLED/OUTCOME_RECORDED mirror the network's
	// advice states.
	AdviceStateAuthorized      = "AUTHORIZED"
	AdviceStateCancelled       = "CANCELLED"
	AdviceStateOutcomeRecorded = "OUTCOME_RECORDED"

	// AdviceOutcomePENDING (composed, not yet accepted by the network),
	// SUBMITTED (the network answered 202, its transaction is settling),
	// SUCCEEDED (the transaction succeeded; terminal) and FAILED (the
	// network refused; terminal) are THIS service's submission states.
	AdviceOutcomePending   = "PENDING"
	AdviceOutcomeSubmitted = "SUBMITTED"
	AdviceOutcomeSucceeded = "SUCCEEDED"
	AdviceOutcomeFailed    = "FAILED"
)

// ReturnAdviceLine is one authorized line of a network return advice: how
// many units of one catalog item may come back, and for which stock owner.
type ReturnAdviceLine struct {
	LineNo        int32
	CatalogItemID string
	Quantity      int64
	// OwnerRef is the stock owner of the line; "" is operator-owned stock.
	OwnerRef string
}

// ReturnAdvice is the network's return authorization as this service's work
// item, keyed AuthorizationID (ADR 0012): what the poller saw. Disposition is
// the network's authorization-level decision (an INPUT, recorded, never
// re-decided here); OutcomeState is the local submission state.
type ReturnAdvice struct {
	AuthorizationID string
	OrderID         string
	GroupNo         int64
	VendorID        string
	NodeID          string
	Disposition     string
	Method          string
	State           string
	Lines           []ReturnAdviceLine
	TrackingNumbers []string
	ReadyAt         time.Time
	ExpiresAt       time.Time
	FirstSeenAt     time.Time
	// OutcomeState is "" until a receipt against this advice completes, then
	// PENDING, SUBMITTED or FAILED.
	OutcomeState string
}

// ReturnOutcomeLine is one graded line of the outcome: restockable units may
// go back to sellable stock, unsellable ones may not.
type ReturnOutcomeLine struct {
	CatalogItemID string
	// OwnerRef is the stock owner the quantities belong to; "" is
	// operator-owned stock.
	OwnerRef       string
	QtyRestockable int64
	QtyUnsellable  int64
}

// ReturnOutcome is the graded result this service reports to the network for
// one advice, composed from a completed receipt's disposition decisions.
type ReturnOutcome struct {
	AuthorizationID string
	NodeID          string
	ReceivedAt      time.Time
	Lines           []ReturnOutcomeLine
}

// PendingReturnOutcome is one stored outcome awaiting submission (no
// transaction yet) or settlement (the network answered 202 and its
// transaction is not terminal), oldest first.
type PendingReturnOutcome struct {
	Outcome       ReturnOutcome
	TransactionID string
	// PendingSince orders the drain (the instant the outcome was composed).
	PendingSince time.Time
}

// The network's transaction verdicts of an asynchronous write.
const (
	TransactionProcessing = "PROCESSING"
	TransactionSucceeded  = "SUCCEEDED"
	TransactionFailed     = "FAILED"
)

// ReturnOutcomeTransaction is the network's verdict of a submitted outcome.
type ReturnOutcomeTransaction struct {
	Status string
	// Errors is the network's refusal text (joined problem codes), "" unless
	// the status is FAILED.
	Errors string
}
