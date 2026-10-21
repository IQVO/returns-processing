// Package returnreceipt holds the ReturnReceipt aggregate (ADR 0002): the
// physical arrival of returned goods and their inspection. It is pure — no
// I/O, no clock reads; ids and quantities are the shared value objects.
package returnreceipt

import (
	"slices"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// Line is one received line. It is built by MatchedLine or UnexpectedLine
// only, so a line is always exactly one of the two kinds — a zero Line (the
// refused third state) fails Open.
type Line struct {
	lineNo       int32
	sku          shared.ID
	quantity     shared.Quantity
	kind         LineKind
	rmaID        shared.ID
	inspected    bool
	inspectedQty shared.Quantity
	grade        shared.Grade
	reasons      []string
	inspectedAt  time.Time
}

// MatchedLine builds a line that references the RMA line it returns.
func MatchedLine(lineNo int32, sku string, quantity int64, rmaID string) (Line, error) {
	l, err := newLine(lineNo, sku, quantity)
	if err != nil {
		return Line{}, err
	}
	id, err := shared.NewRMAID(rmaID)
	if err != nil {
		return Line{}, err
	}
	l.kind = KindMatched
	l.rmaID = id
	return l, nil
}

// UnexpectedLine builds a line with no RMA: it raises
// UnexpectedReturnReceived downstream.
func UnexpectedLine(lineNo int32, sku string, quantity int64) (Line, error) {
	l, err := newLine(lineNo, sku, quantity)
	if err != nil {
		return Line{}, err
	}
	l.kind = KindUnexpected
	return l, nil
}

func newLine(lineNo int32, sku string, quantity int64) (Line, error) {
	s, err := shared.NewSKU(sku)
	if err != nil {
		return Line{}, err
	}
	q, err := shared.NewQuantity(quantity)
	if err != nil {
		return Line{}, err
	}
	if lineNo < 1 {
		return Line{}, shared.ErrLineNoBelowOne
	}
	return Line{lineNo: lineNo, sku: s, quantity: q}, nil
}

// LineNo returns the line number.
func (l Line) LineNo() int32 { return l.lineNo }

// SKU returns the sku of the line.
func (l Line) SKU() string { return l.sku.String() }

// Quantity returns the received quantity.
func (l Line) Quantity() int64 { return l.quantity.Int64() }

// Kind returns MATCHED or UNEXPECTED.
func (l Line) Kind() LineKind { return l.kind }

// RMAID returns the rma the line matches ("" on an unexpected line).
func (l Line) RMAID() string { return l.rmaID.String() }

// Inspected reports whether the line already has a grade.
func (l Line) Inspected() bool { return l.inspected }

// InspectedQuantity returns the inspected quantity (0 until inspected).
func (l Line) InspectedQuantity() int64 { return l.inspectedQty.Int64() }

// Grade returns the inspection grade ("" until inspected).
func (l Line) Grade() shared.Grade { return l.grade }

// ReasonCodes returns why the grade (nil until inspected).
func (l Line) ReasonCodes() []string { return slices.Clone(l.reasons) }

// Receipt is the aggregate root: one parcel of returned goods at one site.
// Version is 1 when opened and +1 per inspected line.
type Receipt struct {
	id       shared.ID
	rmaID    shared.ID
	site     shared.SiteID
	lines    []Line
	openedAt time.Time
	version  int64
	events   []Event
}

// OpenInput is everything Open needs.
type OpenInput struct {
	ReceiptID string
	// RMAID is the rma the parcel was opened against; "" when the whole
	// parcel is unexpected.
	RMAID  string
	SiteID string
	Lines  []Line
	At     time.Time
}

// Open creates a receipt at version 1, raising ReturnReceiptOpened (all
// lines) and one UnexpectedReturnReceived per UNEXPECTED line. It refuses a
// line that is neither matched nor unexpected (the third state), a matched
// line without its rma, an unexpected line with one, and duplicate line
// numbers.
func Open(in OpenInput) (*Receipt, error) {
	id, err := shared.NewReceiptID(in.ReceiptID)
	if err != nil {
		return nil, err
	}
	site, err := shared.NewSiteID(in.SiteID)
	if err != nil {
		return nil, err
	}
	if in.At.IsZero() {
		return nil, ErrNoTimestamp
	}
	if len(in.Lines) < 1 {
		return nil, ErrNoLines
	}
	if err := checkLineKinds(in.Lines); err != nil {
		return nil, err
	}
	var rma shared.ID
	if in.RMAID != "" {
		rma, err = shared.NewRMAID(in.RMAID)
		if err != nil {
			return nil, ErrInvalidReceiptRMA
		}
	}
	r := &Receipt{id: id, rmaID: rma, site: site, openedAt: in.At.UTC(), version: 1}
	r.raise(ReturnReceiptOpened{
		Header: Header{Receipt: id.String(), Version: r.version, At: in.At},
		RMAID:  rma.String(),
		SiteID: site.String(),
		Opened: r.openedAt,
		Lines:  openedLines(in.Lines),
	})
	for _, l := range in.Lines {
		if l.kind != KindUnexpected {
			continue
		}
		r.raise(UnexpectedReturnReceived{
			Header:   Header{Receipt: id.String(), Version: r.version, At: in.At},
			LineNo:   l.lineNo,
			SKU:      l.sku.String(),
			Quantity: l.quantity.Int64(),
			SiteID:   site.String(),
			Received: r.openedAt,
		})
	}
	r.lines = slices.Clone(in.Lines)
	return r, nil
}

// Inspect grades one line: inspected qty in [1, received qty], a line
// inspected exactly once. Raises ReturnLineInspected.
func (r *Receipt) Inspect(lineNo int32, grade shared.Grade, quantity int64, reasonCodes []string, at time.Time) error {
	if at.IsZero() {
		return ErrNoTimestamp
	}
	if !grade.Valid() {
		return ErrInvalidGrade
	}
	qty, err := shared.NewQuantity(quantity)
	if err != nil {
		return err
	}
	i := r.indexOfLine(lineNo)
	if i < 0 {
		return ErrLineNotFound
	}
	l := &r.lines[i]
	if l.inspected {
		return ErrLineAlreadyInspected
	}
	if qty.Int64() > l.quantity.Int64() {
		return ErrInspectedExceedsReceived
	}
	l.inspected = true
	l.inspectedQty = qty
	l.grade = grade
	l.reasons = slices.Clone(reasonCodes)
	l.inspectedAt = at.UTC()
	r.version++
	r.raise(ReturnLineInspected{
		Header:      Header{Receipt: r.id.String(), Version: r.version, At: at},
		LineNo:      l.lineNo,
		SKU:         l.sku.String(),
		Quantity:    qty.Int64(),
		Grade:       grade.String(),
		ReasonCodes: slices.Clone(l.reasons),
		Inspected:   l.inspectedAt,
	})
	return nil
}

// DomainEvents returns the facts raised since the aggregate was created, in
// order, for the outbox. The slice is a copy.
func (r *Receipt) DomainEvents() []Event { return slices.Clone(r.events) }

// ID returns the receipt id.
func (r *Receipt) ID() string { return r.id.String() }

// RMAID returns the rma the receipt was opened against ("" when none).
func (r *Receipt) RMAID() string { return r.rmaID.String() }

// SiteID returns the site the parcel arrived at.
func (r *Receipt) SiteID() string { return r.site.String() }

// OpenedAt returns the arrival instant.
func (r *Receipt) OpenedAt() time.Time { return r.openedAt }

// Version returns the aggregate version.
func (r *Receipt) Version() int64 { return r.version }

// Lines returns the received lines.
func (r *Receipt) Lines() []Line { return slices.Clone(r.lines) }

// Line returns the line with the given number (ErrLineNotFound otherwise).
func (r *Receipt) Line(lineNo int32) (Line, error) {
	if i := r.indexOfLine(lineNo); i >= 0 {
		return r.lines[i], nil
	}
	return Line{}, ErrLineNotFound
}

func (r *Receipt) raise(e Event) { r.events = append(r.events, e) }

func (r *Receipt) indexOfLine(lineNo int32) int {
	for i, l := range r.lines {
		if l.lineNo == lineNo {
			return i
		}
	}
	return -1
}

// checkLineKinds enforces the no-third-state rule and the unique line
// numbers.
func checkLineKinds(lines []Line) error {
	seen := make(map[int32]bool, len(lines))
	for _, l := range lines {
		switch l.kind {
		case KindMatched:
			if l.rmaID.IsZero() {
				return ErrMatchedLineWithoutRMA
			}
		case KindUnexpected:
			if !l.rmaID.IsZero() {
				return ErrUnexpectedLineWithRMA
			}
		default:
			return ErrLineKindMissing
		}
		if seen[l.lineNo] {
			return ErrDuplicateLineNo
		}
		seen[l.lineNo] = true
	}
	return nil
}

func openedLines(lines []Line) []OpenedLine {
	out := make([]OpenedLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, OpenedLine{LineNo: l.lineNo, SKU: l.sku.String(), Quantity: l.quantity.Int64(), RMAID: l.rmaID.String()})
	}
	return out
}
