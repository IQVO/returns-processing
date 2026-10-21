package returnauthorization

import (
	"slices"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// RMA is the aggregate root: permission to send one shipped order line back
// (ADR 0002). States: REQUESTED -> AUTHORIZED | REJECTED -> RECEIVED ->
// CLOSED; REJECTED and CLOSED are terminal. Version is 1 when decided and +1
// per accepted transition; it guards every write.
//
// Invariants:
//
//  1. one open RMA per (orderRef, lineNo): Authorize refuses a known open
//     sibling (the use case + a partial unique index close the race);
//  2. authorizable only when a manifested work unit exists for that line and
//     now <= window end;
//  3. quantity in [1, policy.max_units_per_line] (the constructor holds the
//     floor, Authorize the ceiling).
type RMA struct {
	id            shared.ID
	line          shared.OrderLineRef
	sku           shared.ID
	quantity      shared.Quantity
	site          shared.SiteID
	reasonCode    string
	status        Status
	windowEndsAt  time.Time
	decidedAt     time.Time
	rejectionCode RejectionCode
	receivedAt    time.Time
	closedAt      time.Time
	version       int64
	events        []Event
}

// RequestInput is the identity of the return being requested.
type RequestInput struct {
	RMAID      string
	OrderID    string
	LineNo     int32
	SKU        string
	Quantity   int64
	SiteID     string
	ReasonCode string
}

// Request opens a REQUESTED RMA (no event yet; the decision raises one).
func Request(in RequestInput) (*RMA, error) {
	id, err := shared.NewRMAID(in.RMAID)
	if err != nil {
		return nil, err
	}
	orderID, err := shared.NewOrderID(in.OrderID)
	if err != nil {
		return nil, err
	}
	sku, err := shared.NewSKU(in.SKU)
	if err != nil {
		return nil, err
	}
	qty, err := shared.NewQuantity(in.Quantity)
	if err != nil {
		return nil, err
	}
	site, err := shared.NewSiteID(in.SiteID)
	if err != nil {
		return nil, err
	}
	if shared.IsBlank(in.ReasonCode) {
		return nil, ErrBlankReasonCode
	}
	line, err := shared.NewOrderLineRef(orderID, in.LineNo)
	if err != nil {
		return nil, err
	}
	return &RMA{
		id:         id,
		line:       line,
		sku:        sku,
		quantity:   qty,
		site:       site,
		reasonCode: in.ReasonCode,
		status:     StatusRequested,
	}, nil
}

// Facts is what the application layer knows from its event-fed local copies
// (ADR 0003) when the RMA is decided.
type Facts struct {
	// ExistingOpenRMAID is the id of an already-open RMA for the SAME order
	// line ("" when none); the use case reads it from the query that backs
	// the one-open-RMA rule.
	ExistingOpenRMAID string
	// ManifestedWorkUnit is the work unit the manifested copy holds for this
	// line ("" when the line never shipped).
	ManifestedWorkUnit string
	// PolicyMaxUnits is the policy's max_units_per_line cap.
	PolicyMaxUnits int64
	// WindowEndsAt is authorized_at + policy return_window_days.
	WindowEndsAt time.Time
	// Now is the decision instant.
	Now time.Time
}

// Authorize decides a REQUESTED RMA positively: REQUESTED -> AUTHORIZED,
// version 1, raising ReturnAuthorized. It enforces invariants 1 to 3.
func (r *RMA) Authorize(f Facts) error {
	if r.status != StatusRequested {
		return transitionError(r.status, ErrAlreadyDecided)
	}
	if err := checkAuthorizationFacts(r, f); err != nil {
		return err
	}
	r.status = StatusAuthorized
	r.windowEndsAt = f.WindowEndsAt.UTC()
	r.decidedAt = f.Now.UTC()
	r.version = 1
	r.raise(ReturnAuthorized{
		Header:     Header{RMA: r.id.String(), Version: r.version, At: f.Now},
		OrderRef:   r.line,
		SKU:        r.sku.String(),
		Quantity:   r.quantity.Int64(),
		Site:       r.site.String(),
		ReasonCode: r.reasonCode,
		WindowEnds: r.windowEndsAt,
		Authorized: r.decidedAt,
	})
	return nil
}

// Reject decides a REQUESTED RMA negatively: REQUESTED -> REJECTED, version
// 1, raising ReturnAuthorizationRejected. REJECTED is terminal.
func (r *RMA) Reject(code RejectionCode, at time.Time) error {
	if r.status != StatusRequested {
		return transitionError(r.status, ErrAlreadyDecided)
	}
	if !code.Valid() {
		return ErrInvalidRejectionCode
	}
	if at.IsZero() {
		return ErrNoTimestamp
	}
	r.status = StatusRejected
	r.rejectionCode = code
	r.decidedAt = at.UTC()
	r.version = 1
	r.raise(ReturnAuthorizationRejected{
		Header:        Header{RMA: r.id.String(), Version: r.version, At: at},
		OrderRef:      r.line,
		SKU:           r.sku.String(),
		Quantity:      r.quantity.Int64(),
		RejectionCode: code,
		RejectedAt:    r.decidedAt,
	})
	return nil
}

// MarkReceived moves an AUTHORIZED RMA to RECEIVED when its goods arrive
// (the receipt's own event, ReturnReceiptOpened, carries the fact).
func (r *RMA) MarkReceived(at time.Time) error {
	if r.status == StatusRejected || r.status == StatusClosed {
		return transitionError(r.status, ErrTerminal)
	}
	if r.status != StatusAuthorized {
		return ErrNotAuthorized
	}
	if at.IsZero() {
		return ErrNoTimestamp
	}
	r.status = StatusReceived
	r.receivedAt = at.UTC()
	r.version++
	return nil
}

// Close moves a RECEIVED RMA to CLOSED (its lines are dispositioned).
// CLOSED is terminal.
func (r *RMA) Close(at time.Time) error {
	if r.status == StatusRejected || r.status == StatusClosed {
		return transitionError(r.status, ErrTerminal)
	}
	if r.status != StatusReceived {
		return ErrNotReceived
	}
	if at.IsZero() {
		return ErrNoTimestamp
	}
	r.status = StatusClosed
	r.closedAt = at.UTC()
	r.version++
	return nil
}

// Open reports whether the RMA still counts against the one-open-RMA-per-line
// rule (REQUESTED, AUTHORIZED or RECEIVED).
func (r *RMA) Open() bool {
	switch r.status {
	case StatusRequested, StatusAuthorized, StatusReceived:
		return true
	default:
		return false
	}
}

// DomainEvents returns the facts raised since the aggregate was created, in
// order, for the outbox. A restored RMA has none. The slice is a copy.
func (r *RMA) DomainEvents() []Event { return slices.Clone(r.events) }

// ID returns the rma id.
func (r *RMA) ID() string { return r.id.String() }

// Line returns the order line the RMA authorizes.
func (r *RMA) Line() shared.OrderLineRef { return r.line }

// SKU returns the sku coming back.
func (r *RMA) SKU() string { return r.sku.String() }

// Quantity returns the authorized (or requested) quantity.
func (r *RMA) Quantity() int64 { return r.quantity.Int64() }

// SiteID returns the site the goods come back to.
func (r *RMA) SiteID() string { return r.site.String() }

// ReasonCode returns the operator's return reason.
func (r *RMA) ReasonCode() string { return r.reasonCode }

// Status returns the lifecycle state.
func (r *RMA) Status() Status { return r.status }

// RejectionCode returns why the RMA was rejected ("" unless REJECTED).
func (r *RMA) RejectionCode() RejectionCode { return r.rejectionCode }

// WindowEndsAt returns the window end (zero time unless AUTHORIZED or later).
func (r *RMA) WindowEndsAt() time.Time { return r.windowEndsAt }

// Version returns the aggregate version.
func (r *RMA) Version() int64 { return r.version }

func (r *RMA) raise(e Event) { r.events = append(r.events, e) }

func transitionError(s Status, err error) error {
	if s == StatusRejected || s == StatusClosed {
		return ErrTerminal
	}
	return err
}

// checkAuthorizationFacts enforces invariants 1 to 3 in a fixed order, so the
// rejection code is deterministic when several facts fail at once.
func checkAuthorizationFacts(r *RMA, f Facts) error {
	if !shared.IsBlank(f.ExistingOpenRMAID) {
		return ErrOpenRMAExists
	}
	if f.ManifestedWorkUnit == "" || f.ManifestedWorkUnit != r.line.WorkUnit() {
		return ErrLineNotManifested
	}
	if f.WindowEndsAt.IsZero() {
		return ErrNoWindow
	}
	if f.Now.IsZero() {
		return ErrNoTimestamp
	}
	if f.Now.After(f.WindowEndsAt) {
		return ErrWindowExpired
	}
	if f.PolicyMaxUnits < 1 {
		return ErrInvalidMaxUnits
	}
	if r.quantity.Int64() > f.PolicyMaxUnits {
		return ErrQuantityExceedsPolicy
	}
	return nil
}
