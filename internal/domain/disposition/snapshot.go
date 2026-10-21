package disposition

import (
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// Snapshot is the persisted form of a Disposition: the columns of the
// dispositions table, scalars only. Restore validates it.
type Snapshot struct {
	ID            string
	ReceiptID     string
	LineNo        int32
	SKU           string
	Quantity      int64
	Grade         shared.Grade
	Outcome       Outcome
	PolicyVersion int64
	Supersedes    string // "" on a policy decision
	Reason        string // "" on a policy decision
	SiteID        string
	RMAID         string // "" when the line was UNEXPECTED
	ReceivedAt    time.Time
	DecidedAt     time.Time
	Staged        bool
	Version       int64
}

// Snapshot returns the persisted form of the disposition (no events: a
// restored disposition has none pending).
func (d *Disposition) Snapshot() Snapshot {
	return Snapshot{
		ID:            d.id.String(),
		ReceiptID:     d.receiptID.String(),
		LineNo:        d.lineNo,
		SKU:           d.sku.String(),
		Quantity:      d.quantity.Int64(),
		Grade:         d.grade,
		Outcome:       d.outcome,
		PolicyVersion: d.policyVersion,
		Supersedes:    d.supersedes.String(),
		Reason:        d.reason,
		SiteID:        d.site.String(),
		RMAID:         d.rmaID.String(),
		ReceivedAt:    d.receivedAt,
		DecidedAt:     d.decidedAt,
		Staged:        d.staged,
		Version:       d.version,
	}
}

// Restore rebuilds a persisted disposition, validating every invariant the
// snapshot can show, so a corrupt row can never become a live aggregate. The
// restored disposition has no pending events (a restored RESTOCK carries
// staged=true instead: its ReturnLineStaged fact was already published).
func Restore(s Snapshot) (*Disposition, error) {
	if err := validateSnapshot(s); err != nil {
		return nil, err
	}
	id, err := shared.NewDispositionID(s.ID)
	if err != nil {
		return nil, err
	}
	receiptID, err := shared.NewReceiptID(s.ReceiptID)
	if err != nil {
		return nil, err
	}
	sku, err := shared.NewSKU(s.SKU)
	if err != nil {
		return nil, err
	}
	quantity, err := shared.NewQuantity(s.Quantity)
	if err != nil {
		return nil, err
	}
	site, err := shared.NewSiteID(s.SiteID)
	if err != nil {
		return nil, err
	}
	rma, err := optionalRMAID(s.RMAID)
	if err != nil {
		return nil, err
	}
	supersedes, err := optionalDispositionID(s.Supersedes)
	if err != nil {
		return nil, err
	}
	return &Disposition{
		id:            id,
		receiptID:     receiptID,
		lineNo:        s.LineNo,
		sku:           sku,
		quantity:      quantity,
		grade:         s.Grade,
		outcome:       s.Outcome,
		policyVersion: s.PolicyVersion,
		supersedes:    supersedes,
		reason:        s.Reason,
		site:          site,
		rmaID:         rma,
		receivedAt:    s.ReceivedAt.UTC(),
		decidedAt:     s.DecidedAt.UTC(),
		staged:        s.Staged,
		version:       s.Version,
	}, nil
}

// validateSnapshot holds the state pairings a persisted disposition must
// satisfy beyond the value objects: a 1-based line number, a known grade and
// outcome, a declared policy version, both stamps, staging only on a RESTOCK
// and the immutable version 1.
func validateSnapshot(s Snapshot) error {
	if s.LineNo < 1 {
		return shared.ErrLineNoBelowOne
	}
	if !s.Grade.Valid() {
		return ErrInvalidGrade
	}
	if !s.Outcome.Valid() {
		return ErrInvalidForcedOutcome
	}
	if s.PolicyVersion < 1 {
		return ErrInvalidPolicy
	}
	if s.ReceivedAt.IsZero() || s.DecidedAt.IsZero() {
		return ErrNoTimestamp
	}
	if s.Staged && s.Outcome != OutcomeRestock {
		return ErrNotRestock
	}
	if s.Version != 1 {
		return ErrInvalidPolicy
	}
	return nil
}

// optionalRMAID validates the line's rma reference ("" when UNEXPECTED).
func optionalRMAID(value string) (shared.ID, error) {
	if value == "" {
		return shared.ID{}, nil
	}
	return shared.NewRMAID(value)
}

// optionalDispositionID validates the superseded reference ("" on a policy
// decision).
func optionalDispositionID(value string) (shared.ID, error) {
	if value == "" {
		return shared.ID{}, nil
	}
	return shared.NewDispositionID(value)
}

// PolicySnapshot is the persisted form of one DispositionPolicy version.
type PolicySnapshot struct {
	Version          int64
	ReturnWindowDays int32
	MaxUnitsPerLine  int64
	VetoOutcome      Outcome
	Rules            []Rule
	DeclaredAt       time.Time
}

// Snapshot returns the persisted form of the policy version.
func (p *DispositionPolicy) Snapshot() PolicySnapshot {
	return PolicySnapshot{
		Version:          p.version,
		ReturnWindowDays: p.returnWindowDays,
		MaxUnitsPerLine:  p.maxUnitsPerLine,
		VetoOutcome:      p.vetoOutcome,
		Rules:            append([]Rule(nil), p.rules...),
		DeclaredAt:       p.declaredAt,
	}
}

// RestorePolicy rebuilds a persisted policy version. It routes through
// Declare, so the full policy validation (bounds, veto outcome, the total,
// duplicate-free rule table) applies to a stored row exactly as it applied
// when the version was declared.
func RestorePolicy(s PolicySnapshot) (*DispositionPolicy, error) {
	return Declare(PolicyInput(s))
}
