package returnreceipt

import (
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// Snapshot is the persisted form of a receipt: the root columns plus one
// LineSnapshot per line, scalars only. Restore validates it.
type Snapshot struct {
	ID       string
	RMAID    string // "" when the parcel was opened without an rma
	SiteID   string
	OpenedAt time.Time
	Lines    []LineSnapshot
	Version  int64
}

// LineSnapshot is the persisted form of one received line.
type LineSnapshot struct {
	LineNo            int32
	SKU               string
	Quantity          int64
	Kind              LineKind
	RMAID             string // "" on an UNEXPECTED line
	Inspected         bool
	InspectedQuantity int64 // 0 until inspected
	Grade             shared.Grade
	ReasonCodes       []string
	InspectedAt       time.Time
}

// Snapshot returns the persisted form of the receipt (no events: a restored
// receipt has none pending).
func (r *Receipt) Snapshot() Snapshot {
	lines := make([]LineSnapshot, 0, len(r.lines))
	for _, l := range r.lines {
		lines = append(lines, LineSnapshot{
			LineNo:            l.lineNo,
			SKU:               l.sku.String(),
			Quantity:          l.quantity.Int64(),
			Kind:              l.kind,
			RMAID:             l.rmaID.String(),
			Inspected:         l.inspected,
			InspectedQuantity: l.inspectedQty.Int64(),
			Grade:             l.grade,
			ReasonCodes:       l.reasons,
			InspectedAt:       l.inspectedAt,
		})
	}
	return Snapshot{
		ID:       r.id.String(),
		RMAID:    r.rmaID.String(),
		SiteID:   r.site.String(),
		OpenedAt: r.openedAt,
		Lines:    lines,
		Version:  r.version,
	}
}

// Restore rebuilds a persisted receipt, validating every invariant the
// snapshot can show (ids and quantities through the shared value objects,
// the no-third-state line rule, unique line numbers, the inspection pairing),
// so a corrupt row can never become a live aggregate. The restored receipt
// has no pending events.
func Restore(s Snapshot) (*Receipt, error) {
	id, err := shared.NewReceiptID(s.ID)
	if err != nil {
		return nil, err
	}
	site, err := shared.NewSiteID(s.SiteID)
	if err != nil {
		return nil, err
	}
	if s.OpenedAt.IsZero() {
		return nil, ErrNoTimestamp
	}
	if len(s.Lines) < 1 {
		return nil, ErrNoLines
	}
	var rma shared.ID
	if s.RMAID != "" {
		rma, err = shared.NewRMAID(s.RMAID)
		if err != nil {
			return nil, ErrInvalidReceiptRMA
		}
	}
	if s.Version < 1 {
		return nil, ErrNoTimestamp
	}
	lines, err := restoreLines(s.Lines)
	if err != nil {
		return nil, err
	}
	return &Receipt{
		id:       id,
		rmaID:    rma,
		site:     site,
		lines:    lines,
		openedAt: s.OpenedAt.UTC(),
		version:  s.Version,
	}, nil
}

// restoreLines validates and rebuilds the lines, refusing duplicate line
// numbers.
func restoreLines(in []LineSnapshot) ([]Line, error) {
	lines := make([]Line, 0, len(in))
	seen := make(map[int32]bool, len(in))
	for _, ls := range in {
		if seen[ls.LineNo] {
			return nil, ErrDuplicateLineNo
		}
		seen[ls.LineNo] = true
		line, err := restoreLine(ls)
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// restoreLine validates and rebuilds ONE line: sku and quantity through the
// value objects, a 1-based line number, exactly one of the two kinds with
// its rma pairing, and a complete-or-absent inspection.
func restoreLine(ls LineSnapshot) (Line, error) {
	sku, err := shared.NewSKU(ls.SKU)
	if err != nil {
		return Line{}, err
	}
	quantity, err := shared.NewQuantity(ls.Quantity)
	if err != nil {
		return Line{}, err
	}
	if ls.LineNo < 1 {
		return Line{}, shared.ErrLineNoBelowOne
	}
	if !ls.Kind.Valid() {
		return Line{}, ErrLineKindMissing
	}
	rma, err := restoreLineRMA(ls)
	if err != nil {
		return Line{}, err
	}
	inspectedQty, err := restoreInspection(ls)
	if err != nil {
		return Line{}, err
	}
	return Line{
		lineNo:       ls.LineNo,
		sku:          sku,
		quantity:     quantity,
		kind:         ls.Kind,
		rmaID:        rma,
		inspected:    ls.Inspected,
		inspectedQty: inspectedQty,
		grade:        ls.Grade,
		reasons:      ls.ReasonCodes,
		inspectedAt:  ls.InspectedAt,
	}, nil
}

// restoreLineRMA enforces the no-third-state pairing: a MATCHED line carries
// its rma id, an UNEXPECTED one carries none.
func restoreLineRMA(ls LineSnapshot) (shared.ID, error) {
	switch ls.Kind {
	case KindMatched:
		id, err := shared.NewRMAID(ls.RMAID)
		if err != nil {
			return shared.ID{}, ErrMatchedLineWithoutRMA
		}
		return id, nil
	default: // KindUnexpected: Valid was checked
		if ls.RMAID != "" {
			return shared.ID{}, ErrUnexpectedLineWithRMA
		}
		return shared.ID{}, nil
	}
}

// restoreInspection validates the inspection facts of one line: an inspected
// line carries a valid grade, a positive inspected quantity no greater than
// the received quantity and the inspection instant; an uninspected one
// carries none of them.
func restoreInspection(ls LineSnapshot) (shared.Quantity, error) {
	if !ls.Inspected {
		if gradeOrStampsLeftover(ls) {
			return shared.Quantity{}, ErrLineAlreadyInspected
		}
		return shared.Quantity{}, nil
	}
	if !ls.Grade.Valid() {
		return shared.Quantity{}, ErrInvalidGrade
	}
	qty, err := shared.NewQuantity(ls.InspectedQuantity)
	if err != nil {
		return shared.Quantity{}, err
	}
	if qty.Int64() > ls.Quantity {
		return shared.Quantity{}, ErrInspectedExceedsReceived
	}
	if ls.InspectedAt.IsZero() {
		return shared.Quantity{}, ErrNoTimestamp
	}
	return qty, nil
}

// gradeOrStampsLeftover reports whether an uninspected line still carries
// inspection facts, which a persisted row can never show.
func gradeOrStampsLeftover(ls LineSnapshot) bool {
	return ls.Grade != "" || !ls.InspectedAt.IsZero() || ls.InspectedQuantity != 0
}
