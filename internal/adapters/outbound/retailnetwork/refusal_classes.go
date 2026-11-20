package retailnetwork

import "github.com/claudioed/returns-processing/internal/application/ports"

// The anti-corruption layer's reading of the network's refusals (ADR 0013).
//
// The network (vendor-connectivity) declares a replay class for every problem type it
// answers on an idempotent route (its ADR 0015). A RETRYABLE refusal is RELEASED: nothing
// was committed, the Idempotency-Key is free, and the same request under the SAME key
// executes again and may pass. A FINAL or OUTCOME refusal is stored and replayed for 24 h,
// so retrying it only repeats it. The class is the callee's knowledge and lives here, in
// one visible table, derived from the callee's class table; the drain above only sees
// ports.OutcomeRefusal.Released.
//
// A slug that is not in the table is TERMINAL: an unknown refusal must never loop, and a
// 4xx without a problem document has no slug at all. refusal_spec_test.go holds the table
// against the byte-pinned copy of the callee's OpenAPI (contracts/pinned.json).

// refusalClass is how this service treats one problem slug of the network.
type refusalClass uint8

const (
	// classUnset is the zero value: the slug is not in the table, which reads as terminal.
	classUnset refusalClass = iota
	// classTerminal is a FINAL or OUTCOME refusal of the network, or one this caller
	// can never get past: the outcome is recorded FAILED.
	classTerminal
	// classRetryable is a RETRYABLE refusal of the network: the outcome stays PENDING.
	classRetryable
)

// refusalTable maps a problem slug of the network to its class here.
type refusalTable map[string]refusalClass

// retryable reports whether the network releases the slug under the key.
func (t refusalTable) retryable(slug string) bool { return t[slug] == classRetryable }

// outcomeRefusalError is the error of a 400/404/409/422 answer: a ports.OutcomeRefusal.
// A verdict also matches ports.ErrOutcomeRefused (the drain records it terminal); a
// released refusal does not.
type outcomeRefusalError struct {
	slug     string
	released bool
	cause    error // status, slug and the callee's detail, for the operator
}

func (e *outcomeRefusalError) Error() string       { return e.cause.Error() }
func (e *outcomeRefusalError) ProblemSlug() string { return e.slug }
func (e *outcomeRefusalError) Released() bool      { return e.released }

func (e *outcomeRefusalError) Unwrap() []error {
	if e.released {
		return []error{e.cause}
	}
	return []error{ports.ErrOutcomeRefused, e.cause}
}

var _ ports.OutcomeRefusal = (*outcomeRefusalError)(nil)

// returnOutcomesPath is the one idempotent route this service writes to; the table is
// read for its refusals only (a refusal of a read keeps the verdict it always had).
const returnOutcomesPath = "/vendor/return-outcomes"

// returnOutcomeRefusals follows vendor-connectivity ADR 0015 for POST /vendor/return-outcomes
// (key return-outcome-<authorizationId>, the same body on every attempt).
var returnOutcomeRefusals = refusalTable{
	// RETRYABLE: nothing was committed and the answer depends on state that changes.
	"not-found":          classRetryable, // the return advice is an event-fed fact the network has not consumed yet
	"illegal-transition": classRetryable, // the advice has not reached AUTHORIZED yet
	"concurrent-update":  classRetryable, // another writer won the optimistic lock
	"vendor-suspended":   classRetryable, // a reinstatement lets the same request pass
	// RETRYABLE at the network, not answered by this route today; classified as it classifies them.
	"node-not-owned-by-vendor": classRetryable,
	"already-acknowledged":     classRetryable,
	"label-before-acceptance":  classRetryable,
	"confirm-without-label":    classRetryable,
	"ack-window-out-of-range":  classRetryable,

	// TERMINAL: validation-failed is RETRYABLE at the network (one slug for a malformed
	// body and for a state-dependent check, its ADR 0015 judgement 1) but on this route
	// it is a function of our request, which is the same on every attempt: it cannot pass.
	"validation-failed": classTerminal,
	// FINAL at the network: stored and replayed.
	"partial-acknowledgement": classTerminal,
	"ack-window-closed":       classTerminal,
	// The key problems: this service sends a valid key and the same body every time.
	"idempotency-key-required": classTerminal,
	"idempotency-key-invalid":  classTerminal,
	"idempotency-key-reused":   classTerminal,
}
