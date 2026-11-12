package returnauthorization_test

import (
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
)

// The event header accessors are the outbox's read of every RMA fact: id,
// aggregate version and occurred-at must ride every event unchanged.
func TestEventHeaderAccessors(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	e := returnauthorization.ReturnAuthorized{Header: returnauthorization.Header{RMA: "rma-0001", Version: 7, At: at}}
	if e.RMAID() != "rma-0001" {
		t.Errorf("RMAID() = %q, want rma-0001", e.RMAID())
	}
	if e.RMAVersion() != 7 {
		t.Errorf("RMAVersion() = %d, want 7", e.RMAVersion())
	}
	if !e.OccurredAt().Equal(at) {
		t.Errorf("OccurredAt() = %v, want %v", e.OccurredAt(), at)
	}
}
