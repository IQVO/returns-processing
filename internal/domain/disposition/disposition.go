package disposition

import (
	"slices"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// Disposition is the aggregate root: the immutable decision for one
// inspected line (ADR 0002). An override is a NEW Disposition carrying
// Supersedes and a reason; the original is never mutated. Version is always
// 1 (decisions do not transition).
type Disposition struct {
	id            shared.ID
	receiptID     shared.ID
	lineNo        int32
	sku           shared.ID
	quantity      shared.Quantity
	grade         shared.Grade
	outcome       Outcome
	policyVersion int64
	supersedes    shared.ID
	reason        string
	site          shared.SiteID
	rmaID         shared.ID
	receivedAt    time.Time
	decidedAt     time.Time
	staged        bool
	version       int64
	events        []Event
}

// LineDecision is everything a policy decision needs: the inspected line,
// the classification from the product-master copy and the current policy.
type LineDecision struct {
	DispositionID  string
	ReceiptID      string
	LineNo         int32
	SKU            string
	Quantity       int64
	Grade          shared.Grade
	Classification Classification
	Policy         *DispositionPolicy
	SiteID         string
	RMAID          string // "" when the line was UNEXPECTED
	ReceivedAt     time.Time
	DecidedAt      time.Time
}

// DecideForLine runs Decide and records the decision. A RESTOCK outcome
// raises ReturnLineStaged in the same step, exactly once for this
// disposition (idempotent downstream on the disposition id).
func DecideForLine(in LineDecision) (*Disposition, error) {
	outcome, err := Decide(in.Grade, in.Classification, in.Policy)
	if err != nil {
		return nil, err
	}
	d := &Disposition{}
	if err := d.fill(in, outcome); err != nil {
		return nil, err
	}
	if err := d.raiseDecision(outcome); err != nil {
		return nil, err
	}
	return d, nil
}

// OverrideInput is an operator override of an existing decision.
type OverrideInput struct {
	NewDispositionID string
	Outcome          Outcome
	Reason           string
	DecidedAt        time.Time
}

// Override creates a NEW Disposition superseding d, with a mandatory reason.
// It carries the original's line, grade, quantity and policy version (the
// version under discussion); a RESTOCK override raises its own
// ReturnLineStaged.
func (d *Disposition) Override(in OverrideInput) (*Disposition, error) {
	if shared.IsBlank(in.Reason) {
		return nil, ErrOverrideReasonRequired
	}
	if !in.Outcome.Valid() {
		return nil, ErrInvalidPolicy
	}
	if in.DecidedAt.IsZero() {
		return nil, ErrNoTimestamp
	}
	id, err := shared.NewDispositionID(in.NewDispositionID)
	if err != nil {
		return nil, err
	}
	return &Disposition{
		id:            id,
		receiptID:     d.receiptID,
		lineNo:        d.lineNo,
		sku:           d.sku,
		quantity:      d.quantity,
		grade:         d.grade,
		outcome:       in.Outcome,
		policyVersion: d.policyVersion,
		supersedes:    d.id,
		reason:        in.Reason,
		site:          d.site,
		rmaID:         d.rmaID,
		receivedAt:    d.receivedAt,
		decidedAt:     in.DecidedAt.UTC(),
		version:       1,
	}, nil
}

// Stage raises ReturnLineStaged for a RESTOCK disposition. It is the only
// way the event appears, so it is emitted exactly once per RESTOCK
// disposition: a second call is refused (ErrAlreadyStaged), and a
// non-RESTOCK disposition cannot stage at all (ErrNotRestock).
func (d *Disposition) Stage() error {
	if d.outcome != OutcomeRestock {
		return ErrNotRestock
	}
	if d.staged {
		return ErrAlreadyStaged
	}
	d.staged = true
	d.raise(ReturnLineStaged{
		Header:     Header{Disposition: d.id.String(), Version: d.version, At: d.decidedAt},
		ReceiptID:  d.receiptID.String(),
		LineNo:     d.lineNo,
		SKU:        d.sku.String(),
		Quantity:   d.quantity.Int64(),
		Condition:  StagedCondition,
		ReceivedAt: d.receivedAt,
		RMAID:      d.rmaID.String(),
		SiteID:     d.site.String(),
	})
	return nil
}

// DomainEvents returns the facts raised, in order, for the outbox.
// The slice is a copy.
func (d *Disposition) DomainEvents() []Event { return slices.Clone(d.events) }

// ID returns the disposition id.
func (d *Disposition) ID() string { return d.id.String() }

// ReceiptID returns the receipt the line arrived on.
func (d *Disposition) ReceiptID() string { return d.receiptID.String() }

// LineNo returns the receipt line number.
func (d *Disposition) LineNo() int32 { return d.lineNo }

// SKU returns the sku of the line.
func (d *Disposition) SKU() string { return d.sku.String() }

// Quantity returns the decided quantity.
func (d *Disposition) Quantity() int64 { return d.quantity.Int64() }

// Grade returns the inspection grade the decision used.
func (d *Disposition) Grade() shared.Grade { return d.grade }

// Outcome returns RESTOCK, REFURBISH, LIQUIDATE, DISPOSE or RTV.
func (d *Disposition) Outcome() Outcome { return d.outcome }

// PolicyVersion returns the policy version the decision used (an override
// keeps the original's).
func (d *Disposition) PolicyVersion() int64 { return d.policyVersion }

// Supersedes returns the superseded disposition id ("" on a policy decision).
func (d *Disposition) Supersedes() string { return d.supersedes.String() }

// Reason returns the override reason ("" on a policy decision).
func (d *Disposition) Reason() string { return d.reason }

// SiteID returns the site the goods are staged at.
func (d *Disposition) SiteID() string { return d.site.String() }

// RMAID returns the rma the line was authorized against ("" when none).
func (d *Disposition) RMAID() string { return d.rmaID.String() }

// DecidedAt returns the decision instant.
func (d *Disposition) DecidedAt() time.Time { return d.decidedAt }

// Staged reports whether ReturnLineStaged was raised.
func (d *Disposition) Staged() bool { return d.staged }

func (d *Disposition) raise(e Event) { d.events = append(d.events, e) }

// raiseDecision writes the DispositionDecided event and, for a RESTOCK, the
// ReturnLineStaged event in the same step (Stage on a fresh disposition
// cannot fail; the error return keeps that an invariant, not a panic).
func (d *Disposition) raiseDecision(outcome Outcome) error {
	d.raise(DispositionDecided{
		Header:        Header{Disposition: d.id.String(), Version: d.version, At: d.decidedAt},
		ReceiptID:     d.receiptID.String(),
		LineNo:        d.lineNo,
		SKU:           d.sku.String(),
		Quantity:      d.quantity.Int64(),
		Grade:         d.grade.String(),
		Outcome:       outcome,
		PolicyVersion: d.policyVersion,
		Supersedes:    d.supersedes.String(),
		Reason:        d.reason,
		DecidedAt:     d.decidedAt,
	})
	if outcome != OutcomeRestock {
		return nil
	}
	return d.Stage()
}

// fill validates the line decision and populates the aggregate.
func (d *Disposition) fill(in LineDecision, outcome Outcome) error {
	id, err := shared.NewDispositionID(in.DispositionID)
	if err != nil {
		return err
	}
	receiptID, err := shared.NewReceiptID(in.ReceiptID)
	if err != nil {
		return err
	}
	sku, err := shared.NewSKU(in.SKU)
	if err != nil {
		return err
	}
	qty, err := shared.NewQuantity(in.Quantity)
	if err != nil {
		return err
	}
	site, err := shared.NewSiteID(in.SiteID)
	if err != nil {
		return err
	}
	var rma shared.ID
	if in.RMAID != "" {
		rma, err = shared.NewRMAID(in.RMAID)
		if err != nil {
			return err
		}
	}
	if in.LineNo < 1 {
		return shared.ErrLineNoBelowOne
	}
	if in.ReceivedAt.IsZero() || in.DecidedAt.IsZero() {
		return ErrNoTimestamp
	}
	*d = Disposition{
		id:            id,
		receiptID:     receiptID,
		lineNo:        in.LineNo,
		sku:           sku,
		quantity:      qty,
		grade:         in.Grade,
		outcome:       outcome,
		policyVersion: in.Policy.Version(),
		site:          site,
		rmaID:         rma,
		receivedAt:    in.ReceivedAt.UTC(),
		decidedAt:     in.DecidedAt.UTC(),
		version:       1,
	}
	return nil
}
