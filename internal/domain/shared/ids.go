package shared

import (
	"errors"
	"strconv"
	"strings"
)

// Domain errors of the shared value objects (compare with errors.Is).
var (
	// ErrBlankID: an id is empty or only white space.
	ErrBlankID = errors.New("shared: id must not be blank")
	// ErrNULInID: an id contains a NUL byte, which Postgres rejects.
	ErrNULInID = errors.New("shared: id must not contain a NUL byte")
	// ErrQuantityBelowOne: a quantity is below 1.
	ErrQuantityBelowOne = errors.New("shared: quantity must be at least 1")
	// ErrLineNoBelowOne: a line number is below 1.
	ErrLineNoBelowOne = errors.New("shared: line number must be at least 1")
	// ErrInvalidWorkUnit: a work-unit string is not {order_id}-line-{line_no}.
	ErrInvalidWorkUnit = errors.New("shared: work unit must be {order_id}-line-{line_no}")
)

// ID is a validated identifier value object. Every aggregate id (rma, receipt,
// disposition, order, sku) is an ID; the kind only names it in errors. A
// valid ID is never blank and never carries a NUL byte.
type ID struct {
	kind  string
	value string
}

// NewRMAID validates and returns a return-authorization id.
func NewRMAID(value string) (ID, error) { return newID("rma id", value) }

// NewReceiptID validates and returns a return-receipt id.
func NewReceiptID(value string) (ID, error) { return newID("receipt id", value) }

// NewDispositionID validates and returns a disposition id.
func NewDispositionID(value string) (ID, error) { return newID("disposition id", value) }

// NewOrderID validates and returns an order id.
func NewOrderID(value string) (ID, error) { return newID("order id", value) }

// NewSKU validates and returns a SKU.
func NewSKU(value string) (ID, error) { return newID("sku", value) }

func newID(kind, value string) (ID, error) {
	if IsBlank(value) {
		return ID{}, ErrBlankID
	}
	if strings.ContainsRune(value, 0) {
		return ID{}, ErrNULInID
	}
	return ID{kind: kind, value: value}, nil
}

// String returns the id value.
func (i ID) String() string { return i.value }

// IsZero reports whether the ID holds nothing (the zero value).
func (i ID) IsZero() bool { return i.value == "" }

// Kind names the id kind (for error messages).
func (i ID) Kind() string { return i.kind }

// MustID is the test and example helper: it panics on an invalid id.
func MustID(value string) ID {
	id, err := newID("id", value)
	if err != nil {
		panic(err)
	}
	return id
}

// Quantity is a counted amount of units; a valid Quantity is at least 1.
type Quantity struct{ value int64 }

// NewQuantity validates and returns a Quantity of n units.
func NewQuantity(n int64) (Quantity, error) {
	if n < 1 {
		return Quantity{}, ErrQuantityBelowOne
	}
	return Quantity{value: n}, nil
}

// Int64 returns the unit count.
func (q Quantity) Int64() int64 { return q.value }

// IsZero reports whether the Quantity holds nothing (the zero value).
func (q Quantity) IsZero() bool { return q.value == 0 }

// SiteID is a validated warehouse site code.
type SiteID struct{ value string }

// NewSiteID validates and returns a SiteID.
func NewSiteID(value string) (SiteID, error) {
	if IsBlank(value) {
		return SiteID{}, ErrBlankID
	}
	if strings.ContainsRune(value, 0) {
		return SiteID{}, ErrNULInID
	}
	return SiteID{value: value}, nil
}

// String returns the site code.
func (s SiteID) String() string { return s.value }

// Grade is the inspection outcome of a return line: A, B, C or SCRAP. Both
// the receipt and the disposition packages speak it, so it lives here.
type Grade string

// The grades.
const (
	GradeA     Grade = "A"
	GradeB     Grade = "B"
	GradeC     Grade = "C"
	GradeScrap Grade = "SCRAP"
)

// Grades are every valid grade, in order.
var Grades = []Grade{GradeA, GradeB, GradeC, GradeScrap}

// Valid reports whether g is one of the four grades.
func (g Grade) Valid() bool {
	switch g {
	case GradeA, GradeB, GradeC, GradeScrap:
		return true
	default:
		return false
	}
}

// String returns the grade.
func (g Grade) String() string { return string(g) }

// WorkUnitSep separates the order id from the line number in a WES work-unit
// id: `{order_id}-line-{line_no}` (fulfillment-execution's `order_ref`).
const WorkUnitSep = "-line-"

// OrderLineRef identifies one line of one order. Its WorkUnit is the key of
// the manifested-work-unit local copy (ADR 0003).
type OrderLineRef struct {
	orderID ID
	lineNo  int32
}

// NewOrderLineRef validates and returns a reference to orderID's lineNo.
func NewOrderLineRef(orderID ID, lineNo int32) (OrderLineRef, error) {
	if orderID.IsZero() {
		return OrderLineRef{}, ErrBlankID
	}
	if lineNo < 1 {
		return OrderLineRef{}, ErrLineNoBelowOne
	}
	return OrderLineRef{orderID: orderID, lineNo: lineNo}, nil
}

// OrderID returns the order id.
func (r OrderLineRef) OrderID() ID { return r.orderID }

// LineNo returns the line number (1-based).
func (r OrderLineRef) LineNo() int32 { return r.lineNo }

// WorkUnit returns the WES work-unit id `{order_id}-line-{line_no}` — the
// formula fulfillment-execution stamps on PackageManifested.order_ref.
func (r OrderLineRef) WorkUnit() string {
	return r.orderID.value + WorkUnitSep + strconv.FormatInt(int64(r.lineNo), 10)
}

// ParseWorkUnit reverses WorkUnit: it splits s on the LAST `-line-`
// separator (an order id may contain `-line-` itself) and requires a
// 1-based integer line number after it.
func ParseWorkUnit(s string) (OrderLineRef, error) {
	idx := strings.LastIndex(s, WorkUnitSep)
	if idx == -1 {
		return OrderLineRef{}, ErrInvalidWorkUnit
	}
	orderPart, linePart := s[:idx], s[idx+len(WorkUnitSep):]
	orderID, err := NewOrderID(orderPart)
	if err != nil {
		return OrderLineRef{}, ErrInvalidWorkUnit
	}
	lineNo, err := strconv.ParseInt(linePart, 10, 32)
	if err != nil || lineNo < 1 {
		return OrderLineRef{}, ErrInvalidWorkUnit
	}
	return OrderLineRef{orderID: orderID, lineNo: int32(lineNo)}, nil
}
