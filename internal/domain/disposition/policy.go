package disposition

import (
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// Rule is one row of the policy table: for grade and productClass, the
// outcome. productClass "*" is the per-grade default.
type Rule struct {
	Grade        shared.Grade
	ProductClass string
	Outcome      Outcome
}

// PolicyInput declares a new policy version through REST.
type PolicyInput struct {
	Version          int64
	ReturnWindowDays int32
	MaxUnitsPerLine  int64
	VetoOutcome      Outcome // "" defaults to DISPOSE
	Rules            []Rule
	DeclaredAt       time.Time
}

// DispositionPolicy is the versioned operator table (ADR 0002): data, not
// code. Append-only: a change is a new version, never a rewrite.
type DispositionPolicy struct {
	version          int64
	returnWindowDays int32
	maxUnitsPerLine  int64
	vetoOutcome      Outcome
	rules            []Rule
	declaredAt       time.Time
}

// Declare validates and installs a policy version.
func Declare(in PolicyInput) (*DispositionPolicy, error) {
	if in.Version < 1 || in.ReturnWindowDays < 0 || in.MaxUnitsPerLine < 1 {
		return nil, ErrInvalidPolicy
	}
	if in.DeclaredAt.IsZero() {
		return nil, ErrNoTimestamp
	}
	veto := in.VetoOutcome
	if veto == "" {
		veto = OutcomeDispose
	}
	if !veto.Valid() {
		return nil, ErrInvalidVetoOutcome
	}
	if err := validateRules(in.Rules); err != nil {
		return nil, err
	}
	return &DispositionPolicy{
		version:          in.Version,
		returnWindowDays: in.ReturnWindowDays,
		maxUnitsPerLine:  in.MaxUnitsPerLine,
		vetoOutcome:      veto,
		rules:            append([]Rule(nil), in.Rules...),
		declaredAt:       in.DeclaredAt.UTC(),
	}, nil
}

// Version returns the policy version.
func (p *DispositionPolicy) Version() int64 { return p.version }

// ReturnWindowDays returns how long after authorization goods may arrive.
func (p *DispositionPolicy) ReturnWindowDays() int32 { return p.returnWindowDays }

// MaxUnitsPerLine returns the authorized-quantity cap (ADR 0003: the
// shipped-quantity check is planned, not built).
func (p *DispositionPolicy) MaxUnitsPerLine() int64 { return p.maxUnitsPerLine }

// VetoOutcome returns what a guardrail-demoted RESTOCK falls back to.
func (p *DispositionPolicy) VetoOutcome() Outcome { return p.vetoOutcome }

// DeclaredAt returns when this version was declared.
func (p *DispositionPolicy) DeclaredAt() time.Time { return p.declaredAt }

// WindowEndsFrom returns t + return window days (the RMA window end).
func (p *DispositionPolicy) WindowEndsFrom(t time.Time) time.Time {
	return t.AddDate(0, 0, int(p.returnWindowDays))
}

// ruleFor returns the table's outcome for (grade, class): an exact entry
// wins over the "*" default; ok is false when neither exists (a policy that
// passes validateRules always has the default).
func (p *DispositionPolicy) ruleFor(g shared.Grade, class string) (Outcome, bool) {
	var def Outcome
	var hasDef bool
	for _, r := range p.rules {
		if r.Grade != g {
			continue
		}
		if r.ProductClass == class {
			return r.Outcome, true
		}
		if r.ProductClass == AnyProductClass {
			def, hasDef = r.Outcome, true
		}
	}
	return def, hasDef
}

// validateRules refuses an unknown grade, an invalid outcome, a duplicate
// (grade, productClass) pair and a table that is not total (some grade with
// a specific rule but no "*" default, or no rules at all — an empty table
// fails the totality loop below with the same error).
func validateRules(rules []Rule) error {
	seen := make(map[string]bool, len(rules))
	for _, r := range rules {
		if !r.Grade.Valid() {
			return ErrInvalidGrade
		}
		if !r.Outcome.Valid() {
			return ErrInvalidPolicy
		}
		if r.ProductClass == "" {
			return ErrInvalidPolicy
		}
		key := r.Grade.String() + "\x00" + r.ProductClass
		if seen[key] {
			return ErrDuplicateRule
		}
		seen[key] = true
	}
	for _, g := range shared.Grades {
		if _, ok := ruleForG(rules, g, AnyProductClass); !ok {
			return ErrPolicyNotTotal
		}
	}
	return nil
}

func ruleForG(rules []Rule, g shared.Grade, class string) (Outcome, bool) {
	for _, r := range rules {
		if r.Grade == g && r.ProductClass == class {
			return r.Outcome, true
		}
	}
	return "", false
}

// Classification is what the application layer derives from the
// product-master local copy (ADR 0003) and hands to Decide.
type Classification struct {
	SKU string
	// ProductClass is the policy-table key (e.g. "Hazmat", "Standard");
	// "" behaves like an unknown class, which the "*" default catches.
	ProductClass string
	// Hazmat is the handling-tag fact.
	Hazmat bool
	// DoTHazardClass is the DOT hazard class; 0 means the SKU carries no
	// rating.
	DoTHazardClass int
	// TemperatureClass is informational for the operator.
	TemperatureClass string
	// Forces is a mandatory outcome that overrides the table (e.g. DISPOSE
	// for a never-restock SKU); "" when none.
	Forces Outcome
	// Classified is false until ProductClassified arrives (fail-open for
	// grading; the hazmat guardrail still applies once known).
	Classified bool
}

// Decide is the pure policy function: deterministic and total over
// (grade x classification x policy) — the same inputs always give the same
// outcome, and every combination answers.
//
// Order of precedence:
//
//  1. a forced outcome on the classification wins outright;
//  2. the table's outcome for (grade, class), the "*" default otherwise;
//  3. a RESTOCK the table returned is demoted to the policy's veto outcome
//     when the grade is below B, or the SKU is hazmat without a DOT rating
//     (fail-closed, ADR 0001).
func Decide(g shared.Grade, c Classification, p *DispositionPolicy) (Outcome, error) {
	if !g.Valid() {
		return "", ErrInvalidGrade
	}
	if p == nil {
		return "", ErrNoPolicy
	}
	if c.Forces != "" {
		if !c.Forces.Valid() {
			return "", ErrInvalidForcedOutcome
		}
		return c.Forces, nil
	}
	out, ok := p.ruleFor(g, c.ProductClass)
	if !ok {
		return "", ErrPolicyNotTotal
	}
	if out == OutcomeRestock && restockVetoed(g, c) {
		return p.vetoOutcome, nil
	}
	return out, nil
}

func restockVetoed(g shared.Grade, c Classification) bool {
	if g != shared.GradeA && g != shared.GradeB {
		return true
	}
	return c.Hazmat && c.DoTHazardClass <= 0
}
