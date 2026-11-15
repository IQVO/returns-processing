package returnreceipt_test

import (
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
)

// The event header accessors are the outbox's read of every receipt fact:
// id, aggregate version and occurred-at must ride every event unchanged.
func TestEventHeaderAccessors(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	e := returnreceipt.ReturnReceiptOpened{Header: returnreceipt.Header{Receipt: "rcpt-0001", Version: 3, At: at}}
	if e.ReceiptID() != "rcpt-0001" {
		t.Errorf("ReceiptID() = %q, want rcpt-0001", e.ReceiptID())
	}
	if e.ReceiptVersion() != 3 {
		t.Errorf("ReceiptVersion() = %d, want 3", e.ReceiptVersion())
	}
	if !e.OccurredAt().Equal(at) {
		t.Errorf("OccurredAt() = %v, want %v", e.OccurredAt(), at)
	}
}
