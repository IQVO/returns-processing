// Package report is the self-contained read-model region of the analytics
// read side (ADR 0007): the shape of the returns reports (disposition mix,
// value recovery, return cycle time), the query rules (date range, site
// scope, day windows, granularity), the pure percentile and freshness logic,
// the validation of a projectable event, and the writer/reader ports the
// analytical store implements. It imports no other internal package, so the
// OLTP domain can never leak into the projection and the arch test keeps
// this region isolated (mirroring pick-face-replenishment ADR 0008).
package report

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kind is which of the projected events a fact came from. The names are the
// CloudEvents event names (the last segment of `type`).
type Kind string

// The five projected kinds: the receipt lifecycle (open, an unexpected line,
// inspection) and the disposition lifecycle (decision, staging). The two
// authorization events are mirrored on the analytics topic too but answer no
// report and are acknowledged and ignored by the projector.
const (
	KindReturnReceiptOpened      Kind = "ReturnReceiptOpened"
	KindUnexpectedReturnReceived Kind = "UnexpectedReturnReceived"
	KindReturnLineInspected      Kind = "ReturnLineInspected"
	KindDispositionDecided       Kind = "DispositionDecided"
	KindReturnLineStaged         Kind = "ReturnLineStaged"
)

// Kinds returns every projected kind, in lifecycle order.
func Kinds() []Kind {
	return []Kind{KindReturnReceiptOpened, KindUnexpectedReturnReceived, KindReturnLineInspected, KindDispositionDecided, KindReturnLineStaged}
}

// Known reports whether k is one of the five projected kinds.
func (k Kind) Known() bool {
	for _, c := range Kinds() {
		if k == c {
			return true
		}
	}
	return false
}

// IsLine reports whether k identifies one line of one receipt by
// (receipt_id, line_no) — every projected kind but the two disposition
// events, whose subject is the disposition id.
func (k Kind) IsLine() bool {
	return k == KindReturnReceiptOpened || k == KindUnexpectedReturnReceived || k == KindReturnLineInspected
}

// IsDisposition reports whether k's subject is a disposition id.
func (k Kind) IsDisposition() bool {
	return k == KindDispositionDecided || k == KindReturnLineStaged
}

// OpenedLine is one line a ReturnReceiptOpened event carries.
type OpenedLine struct {
	LineNo   int32
	SKU      string
	Quantity int64
	RMAID    string // "" = the line was unexpected
}

// Event is one decoded analytics event ready to project. Which fields are
// set depends on the Kind: the three line kinds carry ReceiptID (the
// CloudEvents subject) and a LineNo (the opened event carries every line of
// the parcel); the two disposition kinds carry DispositionID (the subject)
// and address the line through ReceiptID + LineNo. SiteID comes from the
// receipt events and ReturnLineStaged; Quantity is the line quantity;
// inspected carries Grade; decided carries Outcome and PolicyVersion; staged
// carries RMAID and SiteID.
type Event struct {
	Kind    Kind
	EventID string    // CloudEvents id: the idempotency key
	At      time.Time // CloudEvents time: when the event occurred
	SiteID  string
	SKU     string

	ReceiptID     string
	DispositionID string
	LineNo        int32
	Quantity      int64
	RMAID         string
	Grade         string
	Outcome       string
	PolicyVersion int64
	// Supersedes and Reason are set only by an override decision (nullable
	// in the DispositionDecided payload, "" here when absent).
	Supersedes string
	Reason     string

	// Lines is the parcel content of a ReturnReceiptOpened event.
	Lines []OpenedLine
}

// The value spaces the projected payloads pin (ADR 0004): the inspection
// grades and the disposition outcomes.
const (
	GradeScrap = "SCRAP"
)

// grades are the four inspection grades of the ReturnLineInspected contract.
var grades = map[string]bool{"A": true, "B": true, "C": true, GradeScrap: true}

// outcomes are the five disposition outcomes of the DispositionDecided
// contract.
var outcomes = map[string]bool{"RESTOCK": true, "REFURBISH": true, "LIQUIDATE": true, "DISPOSE": true, "RTV": true}

// ValidGrade reports whether g is one of the four inspection grades.
func ValidGrade(g string) bool { return grades[g] }

// ValidOutcome reports whether o is one of the five disposition outcomes.
func ValidOutcome(o string) bool { return outcomes[o] }

// ErrInvalidEvent marks an event that can never be projected (the consumer
// dead-letters it).
var ErrInvalidEvent = errors.New("report: event cannot be projected")

// Validate checks the invariants every store relies on. Every failure wraps
// ErrInvalidEvent and is deterministic.
func (e Event) Validate() error {
	if err := e.validateEnvelope(); err != nil {
		return err
	}
	return e.validatePayload()
}

// validatePayload checks the per-kind invariants.
func (e Event) validatePayload() error {
	switch e.Kind {
	case KindReturnReceiptOpened:
		return e.validateOpened()
	case KindUnexpectedReturnReceived:
		return e.validateUnexpected()
	case KindReturnLineInspected:
		return e.validateInspected()
	case KindDispositionDecided:
		return e.validateDecided()
	case KindReturnLineStaged:
		return e.validateStaged()
	}
	return nil
}

func (e Event) validateOpened() error {
	if len(e.Lines) == 0 {
		return e.invalid("carries no line")
	}
	for _, l := range e.Lines {
		if l.LineNo < 1 || l.SKU == "" || l.Quantity < 1 {
			return e.invalid(fmt.Sprintf("carries an unusable line %d", l.LineNo))
		}
	}
	return nil
}

func (e Event) validateUnexpected() error {
	if e.LineNo < 1 || e.SKU == "" || e.Quantity < 1 {
		return e.invalid("needs a line_no, a sku and a quantity of at least 1")
	}
	return nil
}

func (e Event) validateInspected() error {
	if e.LineNo < 1 || e.SKU == "" || e.Quantity < 1 || !ValidGrade(e.Grade) {
		return e.invalid("needs a line_no, a sku, a quantity of at least 1 and a grade of A, B, C or SCRAP")
	}
	return nil
}

func (e Event) validateDecided() error {
	if e.ReceiptID == "" || e.LineNo < 1 || e.Quantity < 1 || !ValidGrade(e.Grade) || !ValidOutcome(e.Outcome) || e.PolicyVersion < 1 {
		return e.invalid("needs a receipt line, a grade, an outcome and a policy version of at least 1")
	}
	return nil
}

func (e Event) validateStaged() error {
	if e.ReceiptID == "" || e.LineNo < 1 || e.Quantity < 1 || e.SiteID == "" {
		return e.invalid("needs a receipt line, a quantity of at least 1 and a site_id")
	}
	return nil
}

func (e Event) validateEnvelope() error {
	switch {
	case !e.Kind.Known():
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidEvent, e.Kind)
	case e.EventID == "":
		return fmt.Errorf("%w: %s without an id", ErrInvalidEvent, e.Kind)
	case e.At.IsZero():
		return e.invalid("without a time")
	case e.Kind.IsLine() && e.ReceiptID == "":
		return e.invalid("without a receipt_id")
	case e.Kind.IsDisposition() && e.DispositionID == "":
		return e.invalid("without a disposition_id")
	case !e.Kind.IsLine() && !e.Kind.IsDisposition() && (e.ReceiptID != "" || e.DispositionID != ""):
		return e.invalid("identifies a receipt or disposition")
	}
	return nil
}

func (e Event) invalid(why string) error {
	return fmt.Errorf("%w: %s %s %s", ErrInvalidEvent, e.Kind, e.EventID, why)
}

// Milestones is the "this happened at" candidate an event offers for the
// timestamp columns of a return-line fact (nil where it offers none). A
// store keeps the earliest candidate per milestone, so arrival order never
// matters.
type Milestones struct {
	Opened    *time.Time
	Inspected *time.Time
	Decided   *time.Time
	Staged    *time.Time
}

// MilestonesOf returns the milestone an event marks on its receipt line
// (none for a kind that marks none).
func MilestonesOf(e Event) Milestones {
	at := e.At.UTC()
	var m Milestones
	switch e.Kind {
	case KindReturnReceiptOpened, KindUnexpectedReturnReceived:
		m.Opened = &at
	case KindReturnLineInspected:
		m.Inspected = &at
	case KindDispositionDecided:
		m.Decided = &at
	case KindReturnLineStaged:
		m.Staged = &at
	}
	return m
}

// LineRef identifies the fact row an event folds into: the receipt line for
// the line kinds, the decided/staged event's own receipt line for the
// disposition kinds.
type LineRef struct {
	ReceiptID string
	LineNo    int32
}

// LinesOf returns the fact rows an event folds into: every line of the
// parcel for ReturnReceiptOpened, the single line otherwise.
func LinesOf(e Event) []LineRef {
	if e.Kind == KindReturnReceiptOpened {
		out := make([]LineRef, 0, len(e.Lines))
		for _, l := range e.Lines {
			out = append(out, LineRef{ReceiptID: e.ReceiptID, LineNo: l.LineNo})
		}
		return out
	}
	return []LineRef{{ReceiptID: e.ReceiptID, LineNo: e.LineNo}}
}

// Projection is the WRITER port: Apply records the event id and folds the
// event into the model in ONE transaction. applied is false when the id was
// already recorded (a replay): nothing changed. An error wrapping
// ErrRejected is deterministic (the store can never accept this event); any
// other error is transient and the same event may be retried.
type Projection interface {
	Apply(ctx context.Context, e Event) (applied bool, err error)
}

// ErrRejected marks an event the analytical store deterministically refuses
// (a data-exception or integrity-violation class error).
var ErrRejected = errors.New("report: event rejected by the analytical store")

// Reader is the READER port.
type Reader interface {
	// DispositionMix returns the outcome counts by policy version over the
	// query's range (the decision time window), one row per
	// (policy_version, outcome) pair that has at least one decision.
	DispositionMix(ctx context.Context, q MixQuery) ([]MixRow, error)
	// ValueRecoveryDays returns one dense row per DayWindow of the query,
	// each carrying one entry per site that staged anything in the range's
	// scope, zeros included.
	ValueRecoveryDays(ctx context.Context, q SiteQuery) ([]RecoveryDay, error)
	// CycleTimeDays returns one dense row per DayWindow of the query: how
	// many lines were staged in the window and the receipt-to-staged
	// latency percentiles of those whose receipt the projection also saw.
	CycleTimeDays(ctx context.Context, q SiteQuery) ([]CycleDay, error)
	// LastEventAt is the CloudEvents time of the newest event the projection
	// has applied (nil while it has applied none): the freshness basis.
	LastEventAt(ctx context.Context) (*time.Time, error)
}

// MixQuery selects the disposition-mix rows: the time range (a decision
// counts in the day its DispositionDecided `time` falls in) and the optional
// site scope ("" = every site; a decision whose line has no site yet is out
// of a scoped query).
type MixQuery struct {
	Range  Range
	SiteID string
}

// MixRow is one (policy_version, outcome) cell of the disposition mix.
type MixRow struct {
	PolicyVersion int64
	Outcome       string
	// Lines counts the disposition decisions of the cell.
	Lines int
	// Units sums the quantity of those decisions.
	Units int64
}

// SiteQuery selects the day-bucketed reports: the time range, the optional
// site scope ("" = every site) and the bucket granularity (day only today).
type SiteQuery struct {
	Range       Range
	SiteID      string
	Granularity Granularity
}

// RecoverySite is one site's value-recovery numbers of one day.
type RecoverySite struct {
	SiteID string
	// RestockedLines counts the ReturnLineStaged events whose `time` fell in
	// the window at this site.
	RestockedLines int
	// RestockedUnits sums their quantity.
	RestockedUnits int64
}

// RecoveryDay is one UTC day of the value-recovery report: the staged
// (restocked) lines and units per site, over the day's window (the part of
// the day inside the requested range).
type RecoveryDay struct {
	Day   time.Time
	Sites []RecoverySite
}

// CycleDay is one UTC day of the return-cycle-time report.
type CycleDay struct {
	// Day is the UTC calendar day, at midnight UTC.
	Day time.Time
	// StagedLines counts the lines whose ReturnLineStaged `time` fell in the
	// window.
	StagedLines int
	// CycleTime is the receipt-opened-to-staged time of the lines staged in
	// the window whose receipt the projection also saw. A line staged
	// without a seen receipt opening counts in StagedLines but has no
	// latency.
	CycleTime Timing
}
