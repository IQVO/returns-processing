package http

import "strings"

// logSafe strips CR and LF from a request-derived string before it reaches a
// log record, so a crafted path or header cannot forge a log line
// (CodeQL go/log-injection). Every request-derived value that is logged goes
// through it.
func logSafe(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}
