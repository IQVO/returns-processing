package returnreceipt

import "errors"

// LineKind says how a received line relates to an RMA. There are exactly two
// kinds: Matched (the line carries the rma it matches) and Unexpected (it
// carries none and raises UnexpectedReturnReceived). The constructor refuses
// a third state (ADR 0002).
type LineKind string

// The kinds of a received line.
const (
	KindMatched    LineKind = "MATCHED"
	KindUnexpected LineKind = "UNEXPECTED"
)

// Valid reports whether k is one of the two kinds.
func (k LineKind) Valid() bool {
	if k == KindMatched {
		return true
	}
	return k == KindUnexpected
}

// Domain errors, as sentinel values (compare with errors.Is).
var (
	// ErrLineKindMissing: a line is neither matched nor unexpected — the
	// refused third state.
	ErrLineKindMissing = errors.New("returnreceipt: every line must be matched or unexpected")
	// ErrMatchedLineWithoutRMA: a matched line carries no rma id.
	ErrMatchedLineWithoutRMA = errors.New("returnreceipt: a matched line must carry its rma id")
	// ErrUnexpectedLineWithRMA: an unexpected line carries an rma id.
	ErrUnexpectedLineWithRMA = errors.New("returnreceipt: an unexpected line must not carry an rma id")
	// ErrDuplicateLineNo: two lines of one receipt share a line number.
	ErrDuplicateLineNo = errors.New("returnreceipt: line numbers must be unique")
	// ErrNoLines: a receipt is opened without any line.
	ErrNoLines = errors.New("returnreceipt: a receipt needs at least one line")
	// ErrLineNotFound: no line of the receipt has this number.
	ErrLineNotFound = errors.New("returnreceipt: no such line")
	// ErrLineAlreadyInspected: a line is inspected exactly once (invariant 3).
	ErrLineAlreadyInspected = errors.New("returnreceipt: line is already inspected")
	// ErrInspectedExceedsReceived: inspected qty above the received qty (invariant 2).
	ErrInspectedExceedsReceived = errors.New("returnreceipt: inspected quantity exceeds received quantity")
	// ErrInvalidGrade: the grade is not A, B, C or SCRAP.
	ErrInvalidGrade = errors.New("returnreceipt: grade must be A, B, C or SCRAP")
	// ErrNoTimestamp: the open or inspect instant is the zero time.
	ErrNoTimestamp = errors.New("returnreceipt: a timestamp is required")
	// ErrInvalidReceiptRMA: the receipt-level rma id is present but invalid.
	ErrInvalidReceiptRMA = errors.New("returnreceipt: invalid receipt rma id")
)
