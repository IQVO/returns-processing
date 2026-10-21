package repocontract

import (
	"context"
	"testing"

	"github.com/claudioed/returns-processing/internal/application/ports"
)

// ProcessedEvents runs the id-dedupe guard's contract on both adapters: the
// first Claim of a CloudEvents id wins, every later Claim of the same id is
// a duplicate, and different ids never collide.
func ProcessedEvents(t *testing.T, newStore func(t *testing.T) ports.ProcessedEvents) {
	t.Helper()
	run(t, newStore, map[string]func(t *testing.T, s ports.ProcessedEvents){
		"the first claim of an id wins":         processedFirstClaim,
		"a redelivered id is a duplicate":       processedRedelivery,
		"different ids never collide":           processedDistinct,
		"an empty id is claimed like any other": processedEmpty,
	})
}

func processedFirstClaim(t *testing.T, s ports.ProcessedEvents) {
	claimed, err := s.Claim(context.Background(), "evt-1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatal("the first claim of evt-1 must win")
	}
}

func processedRedelivery(t *testing.T, s ports.ProcessedEvents) {
	if claimed, err := s.Claim(context.Background(), "evt-1"); err != nil || !claimed {
		t.Fatalf("first claim = (%t, %v)", claimed, err)
	}
	claimed, err := s.Claim(context.Background(), "evt-1")
	if err != nil {
		t.Fatalf("redelivered claim: %v", err)
	}
	if claimed {
		t.Fatal("the redelivered evt-1 must read as a duplicate")
	}
}

func processedDistinct(t *testing.T, s ports.ProcessedEvents) {
	for _, id := range []string{"evt-1", "evt-2"} {
		if claimed, err := s.Claim(context.Background(), id); err != nil || !claimed {
			t.Fatalf("claim %s = (%t, %v)", id, claimed, err)
		}
	}
}

func processedEmpty(t *testing.T, s ports.ProcessedEvents) {
	// An empty id never reaches the guard (the use case refuses it first);
	// if one ever does, it must behave like any other key, not error.
	if claimed, err := s.Claim(context.Background(), ""); err != nil || !claimed {
		t.Fatalf("claim of the empty id = (%t, %v)", claimed, err)
	}
	if claimed, err := s.Claim(context.Background(), ""); err != nil || claimed {
		t.Fatalf("second claim of the empty id = (%t, %v), want a duplicate", claimed, err)
	}
}
