package postgres

import (
	"testing"
	"time"
)

// The relay's DB behaviour is proven by the integration tests (build tag
// integration, real Postgres + Kafka); these are the pure parts.

func TestRetryBackoffGrowsExponentiallyAndCaps(t *testing.T) {
	base := time.Second
	if got := retryBackoff(base, 0, 5*time.Minute); got != time.Second {
		t.Errorf("attempts 0 = %v, want 1s", got)
	}
	if got := retryBackoff(base, 1, 5*time.Minute); got != 2*time.Second {
		t.Errorf("attempts 1 = %v, want 2s", got)
	}
	if got := retryBackoff(base, 4, 5*time.Minute); got != 16*time.Second {
		t.Errorf("attempts 4 = %v, want 16s", got)
	}
	if got := retryBackoff(base, 40, 5*time.Minute); got != 5*time.Minute {
		t.Errorf("attempts 40 = %v, want the 5m cap", got)
	}
	if got := retryBackoff(base, -3, 5*time.Minute); got != time.Second {
		t.Errorf("negative attempts = %v, want the base", got)
	}
	if got := retryBackoff(0, 3, 5*time.Minute); got != 8*time.Second {
		t.Errorf("zero base = %v, want the 1s default base doubled thrice", got)
	}
}

func TestRelayOptionsApplyWithSanityBounds(t *testing.T) {
	r := NewOutboxRelay(nil, nil, nil,
		WithInterval(250*time.Millisecond),
		WithBatchSize(7),
		WithMaxAttempts(3),
		WithMaxBackoff(30*time.Second),
		WithInterval(-time.Second), // ignored
		WithBatchSize(0),           // ignored
		WithMaxAttempts(-1),        // ignored
		WithMaxBackoff(0),          // ignored
	)
	if r.interval != 250*time.Millisecond || r.batchSize != 7 || r.maxAttempts != 3 || r.maxBackoff != 30*time.Second {
		t.Fatalf("relay = %+v", r)
	}
	def := NewOutboxRelay(nil, nil, nil)
	if def.interval != time.Second || def.batchSize != 100 || def.maxAttempts != 0 || def.maxBackoff != DefaultOutboxMaxBackoff {
		t.Fatalf("defaults = %+v", def)
	}
	if DefaultOutboxMaxAttempts != 20 {
		t.Fatalf("DefaultOutboxMaxAttempts = %d, want the fleet default 20", DefaultOutboxMaxAttempts)
	}
}
