package disposition_test

import (
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/disposition"
)

// The event header accessors are the outbox's read of every disposition
// fact: id, aggregate version and occurred-at must ride every event
// unchanged.
func TestEventHeaderAccessors(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	e := disposition.DispositionDecided{Header: disposition.Header{Disposition: "disp-0001", Version: 5, At: at}}
	if e.DispositionID() != "disp-0001" {
		t.Errorf("DispositionID() = %q, want disp-0001", e.DispositionID())
	}
	if e.DispositionVersion() != 5 {
		t.Errorf("DispositionVersion() = %d, want 5", e.DispositionVersion())
	}
	if !e.OccurredAt().Equal(at) {
		t.Errorf("OccurredAt() = %v, want %v", e.OccurredAt(), at)
	}
}
