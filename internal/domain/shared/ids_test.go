package shared

import (
	"errors"
	"strings"
	"testing"
)

// The invariant: an id value object rejects blank, white-space-only and
// NUL-carrying values (blank/NUL ids are how silent data loss starts).
func TestIDRejectsBlankAndNUL(t *testing.T) {
	for _, bad := range []string{"", " ", "	\n", " \uFEFF", "ok\x00id"} {
		if _, err := NewRMAID(bad); !errors.Is(err, ErrBlankID) && !errors.Is(err, ErrNULInID) {
			t.Fatalf("NewRMAID(%q) = %v, want ErrBlankID or ErrNULInID", bad, err)
		}
	}
	for _, bad := range []string{"", "  ", "WH\x001"} {
		if _, err := NewSiteID(bad); !errors.Is(err, ErrBlankID) && !errors.Is(err, ErrNULInID) {
			t.Fatalf("NewSiteID(%q) = %v, want ErrBlankID or ErrNULInID", bad, err)
		}
	}
	for _, ctor := range []func(string) (ID, error){NewRMAID, NewReceiptID, NewDispositionID, NewOrderID, NewSKU} {
		id, err := ctor("SKU-1")
		if err != nil || id.String() != "SKU-1" || id.IsZero() {
			t.Fatalf("ctor(SKU-1) = %v, %v; want a valid non-zero id", id, err)
		}
	}
}

// The invariant: a Quantity below 1 is refused (a zero-unit return line or
// disposition is nonsense).
func TestQuantityRejectsBelowOne(t *testing.T) {
	for _, bad := range []int64{0, -1, -100} {
		if _, err := NewQuantity(bad); !errors.Is(err, ErrQuantityBelowOne) {
			t.Fatalf("NewQuantity(%d) = %v, want ErrQuantityBelowOne", bad, err)
		}
	}
	q, err := NewQuantity(1)
	if err != nil || q.Int64() != 1 || q.IsZero() {
		t.Fatalf("NewQuantity(1) = %v, %v; want one unit", q, err)
	}
}

// The invariant: only A, B, C and SCRAP are grades.
func TestGradeValid(t *testing.T) {
	for _, g := range Grades {
		if !g.Valid() {
			t.Fatalf("%q should be valid", g)
		}
	}
	for _, g := range []Grade{"", "a", "D", "scrap", "A "} {
		if g.Valid() {
			t.Fatalf("%q should be invalid", g)
		}
	}
	if GradeA.String() != "A" {
		t.Fatalf("GradeA.String() = %q", GradeA.String())
	}
}

// The invariant: the work-unit formula is exactly {order_id}-line-{line_no}
// and its parser reverses it — the manifested-work-unit copy keys on it.
func TestWorkUnitFormulaAndParser(t *testing.T) {
	ref, err := NewOrderLineRef(MustID("ord-7c9e2f1a"), 1)
	if err != nil {
		t.Fatalf("NewOrderLineRef: %v", err)
	}
	if got, want := ref.WorkUnit(), "ord-7c9e2f1a-line-1"; got != want {
		t.Fatalf("WorkUnit() = %q, want %q", got, want)
	}
	back, err := ParseWorkUnit("ord-7c9e2f1a-line-1")
	if err != nil {
		t.Fatalf("ParseWorkUnit: %v", err)
	}
	if back.OrderID().String() != "ord-7c9e2f1a" || back.LineNo() != 1 {
		t.Fatalf("ParseWorkUnit reversed to %+v", back)
	}
	// An order id that itself contains the separator parses on the LAST one.
	weird, err := ParseWorkUnit("ord-line-2-line-3")
	if err != nil {
		t.Fatalf("ParseWorkUnit(weird): %v", err)
	}
	if weird.OrderID().String() != "ord-line-2" || weird.LineNo() != 3 {
		t.Fatalf("ParseWorkUnit(weird) = %+v", weird)
	}
}

// The failing path: a string that is not a work unit is refused, as is a
// zero or missing line number.
func TestWorkUnitParserRefusesGarbage(t *testing.T) {
	for _, bad := range []string{"", "ord-1", "ord-1-line-", "ord-1-line-0", "ord-1-line-x", "-line-1", "ord\x00-1-line-1", " -line-2"} {
		if _, err := ParseWorkUnit(bad); !errors.Is(err, ErrInvalidWorkUnit) {
			t.Fatalf("ParseWorkUnit(%q) = %v, want ErrInvalidWorkUnit", bad, err)
		}
	}
}

// The failing path: an OrderLineRef needs a real order id and a 1-based line.
func TestOrderLineRefRefusesZeroParts(t *testing.T) {
	if _, err := NewOrderLineRef(ID{}, 1); !errors.Is(err, ErrBlankID) {
		t.Fatalf("no order id: %v", err)
	}
	if _, err := NewOrderLineRef(MustID("ord-1"), 0); !errors.Is(err, ErrLineNoBelowOne) {
		t.Fatalf("line 0: %v", err)
	}
}

// IsSpace is the ECMA-262 rule, not unicode.IsSpace: NEL is not a space,
// FEFF and the line separators are.
func TestIsSpaceIsECMA262(t *testing.T) {
	for _, r := range []rune{' ', '\t', '\n', '\v', '\f', '\r', 0xFEFF, 0x2028, 0x2029, 0x3000} {
		if !IsSpace(r) {
			t.Fatalf("U+%04X should be a space", r)
		}
	}
	if IsSpace(0x85) { // NEL is NOT an ECMA-262 space
		t.Fatal("U+0085 must not be a space")
	}
	if !IsBlank("") || !IsBlank(" 	\uFEFF ") {
		t.Fatal("blank strings must be blank")
	}
	if IsBlank("x") {
		t.Fatal("'x' must not be blank")
	}
	if strings.ContainsRune("", 0) != false {
		t.Fatal("sanity")
	}
}
