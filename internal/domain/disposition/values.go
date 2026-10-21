// Package disposition holds the Disposition aggregate and the
// DispositionPolicy (ADR 0002): the immutable decision per inspected line,
// decided by the pure function Decide(grade, classification, policy) and
// superseded only by an explained override. RESTOCK is the only outcome with
// a downstream effect (ReturnLineStaged).
package disposition

import "errors"

// Outcome is the disposition of one inspected line. Only RESTOCK has a
// downstream effect; the others are recorded for audit.
type Outcome string

// The five outcomes.
const (
	OutcomeRestock   Outcome = "RESTOCK"
	OutcomeRefurbish Outcome = "REFURBISH"
	OutcomeLiquidate Outcome = "LIQUIDATE"
	OutcomeDispose   Outcome = "DISPOSE"
	OutcomeRTV       Outcome = "RTV"
)

// Valid reports whether o is one of the five outcomes.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeRestock, OutcomeRefurbish, OutcomeLiquidate, OutcomeDispose, OutcomeRTV:
		return true
	default:
		return false
	}
}

// String returns the outcome.
func (o Outcome) String() string { return string(o) }

// AnyProductClass is the per-grade default rule key: the rule that matches
// every classification the table does not name explicitly.
const AnyProductClass = "*"

// Domain errors, as sentinel values (compare with errors.Is).
var (
	// ErrNoPolicy: Decide was called without a policy.
	ErrNoPolicy = errors.New("disposition: a disposition policy is required")
	// ErrPolicyNotTotal: the table lacks a "*" default for some grade, so
	// Decide would not be total.
	ErrPolicyNotTotal = errors.New("disposition: every grade needs a \"*\" default rule")
	// ErrInvalidPolicy: the policy bounds are out of range.
	ErrInvalidPolicy = errors.New("disposition: policy is invalid")
	// ErrDuplicateRule: two rules share (grade, productClass).
	ErrDuplicateRule = errors.New("disposition: duplicate (grade, productClass) rule")
	// ErrInvalidGrade: the grade is not A, B, C or SCRAP.
	ErrInvalidGrade = errors.New("disposition: grade must be A, B, C or SCRAP")
	// ErrInvalidForcedOutcome: the classification forces an unknown outcome.
	ErrInvalidForcedOutcome = errors.New("disposition: forced outcome is not one of the five")
	// ErrInvalidVetoOutcome: the policy's veto outcome is not one of the five.
	ErrInvalidVetoOutcome = errors.New("disposition: veto outcome is not one of the five")
	// ErrOverrideReasonRequired: an override without a reason is refused.
	ErrOverrideReasonRequired = errors.New("disposition: an override must carry a non-empty reason")
	// ErrNothingToSupersede: an override without its original disposition.
	ErrNothingToSupersede = errors.New("disposition: an override needs the disposition it supersedes")
	// ErrNoTimestamp: the decision instant is the zero time.
	ErrNoTimestamp = errors.New("disposition: a timestamp is required")
	// ErrNotRestock: only a RESTOCK disposition stages a line.
	ErrNotRestock = errors.New("disposition: only a restock stages a line")
	// ErrAlreadyStaged: a RESTOCK disposition stages exactly once.
	ErrAlreadyStaged = errors.New("disposition: line is already staged")
)
