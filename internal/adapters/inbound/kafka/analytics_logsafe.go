package kafka

import "strings"

// logSafe strips CR and LF from a message-derived string before it reaches a
// log record, so a crafted CloudEvents type or id cannot forge a log line
// (CodeQL go/log-injection).
func logSafe(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}
