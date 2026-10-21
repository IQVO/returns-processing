package memory

import (
	"context"
	"sync"
)

// ProcessedEvents is the ports.ProcessedEvents double: a set of the
// CloudEvents ids already applied. It has no transaction awareness (the
// memory wiring runs unwrapped); the same-tx-as-the-effect rule is the
// Postgres adapter's, proven by its integration tests.
type ProcessedEvents struct {
	mu     sync.Mutex
	events map[string]struct{}
}

// NewProcessedEvents returns an empty double.
func NewProcessedEvents() *ProcessedEvents {
	return &ProcessedEvents{events: map[string]struct{}{}}
}

// Claim records eventID and reports whether this call claimed it first.
func (p *ProcessedEvents) Claim(_ context.Context, eventID string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.events[eventID]; ok {
		return false, nil
	}
	p.events[eventID] = struct{}{}
	return true, nil
}
