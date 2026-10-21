// Package shared holds the tiny value rules every domain package of this
// context agrees on: validated ids (blank and NUL are refused), Quantity,
// SiteID, Grade and the OrderLineRef work-unit formula
// `{order_id}-line-{line_no}` (ADR 0002). It imports nothing but the
// standard library.
package shared

import "unicode"

// IsSpace reports whether r is white space or a line terminator in the
// ECMA-262 sense, which is what the `\s` of the OpenAPI `pattern: '\S'` means.
// It is NOT unicode.IsSpace: U+0085 (NEL) is not an ECMA-262 space, U+FEFF
// (the byte order mark) is.
func IsSpace(r rune) bool {
	if r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == 0xFEFF {
		return true
	}
	if r == 0x2028 || r == 0x2029 {
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// IsBlank reports whether s is empty or holds only ECMA-262 white space, i.e.
// whether it fails the contract's `pattern: '\S'`. Use it instead of
// strings.TrimSpace, which disagrees on U+0085 and U+FEFF.
func IsBlank(s string) bool {
	for _, r := range s {
		if !IsSpace(r) {
			return false
		}
	}
	return true
}
