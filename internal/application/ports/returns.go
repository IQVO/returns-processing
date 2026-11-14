package ports

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/returns-processing/internal/application/views"
)

// The network return-advice seam of ADR 0012 — this service's first
// vendor-connectivity integration. The poller feeds a ReturnAdviceView local
// read model keyed authorizationId (a poll-fed equivalent of the ADR 0003
// event-fed copies: the provider offers us no topic), and the graded outcome
// of a completed receipt goes back through the same seam.

// ErrAdviceUnknown is the fail-closed miss of the advice view: no advice with
// that authorizationId was ever seen by the poller.
var ErrAdviceUnknown = errors.New("ports: return advice unknown")

// ErrOutcomeAlreadyStored guards the exactly-once composition rule: an
// advice's outcome is composed from its first completed receipt and never
// replaced (ADR 0012's multi-parcel gap is recorded there).
var ErrOutcomeAlreadyStored = errors.New("ports: return outcome already stored")

// ErrOutcomeRefused is the definitive refusal of an outcome submission: the
// network answered and the same write can never succeed (its problem
// document is in the wrapping error). Anything else — a transport failure, a
// timeout — is retried on the next drain.
var ErrOutcomeRefused = errors.New("ports: return outcome refused")

// ReturnAdviceFeed is the write side of the ReturnAdviceView local read
// model (ADR 0012): the poller upserts the network-owned facts; the receipt
// completion hook stores the composed outcome and the drain records the
// network's answers. Inside a UnitOfWork it joins the transaction in ctx.
type ReturnAdviceFeed interface {
	// ApplyReturnAdvice upserts the network-owned facts of an advice (first
	// sight or re-delivery), keyed authorizationId. The outcome columns are
	// this service's and are never touched here.
	ApplyReturnAdvice(ctx context.Context, a views.ReturnAdvice) error
	// MarkOutcomePending stores out as the advice's PENDING outcome, for the
	// drain to submit. ErrAdviceUnknown when the advice was never seen;
	// ErrOutcomeAlreadyStored when an outcome is already stored.
	MarkOutcomePending(ctx context.Context, authorizationID string, out views.ReturnOutcome) error
	// MarkOutcomeSubmitted records the network's transaction id of an
	// accepted (202) submission: the outcome is SUBMITTED, settling.
	MarkOutcomeSubmitted(ctx context.Context, authorizationID string, transactionID string) error
	// MarkOutcomeSucceeded records the transaction's success as terminal;
	// the outcome leaves the drain. Idempotent (a replayed verdict is a
	// no-op).
	MarkOutcomeSucceeded(ctx context.Context, authorizationID string) error
	// MarkOutcomeFailed records the network's refusal as terminal.
	MarkOutcomeFailed(ctx context.Context, authorizationID string, refusal string) error
}

// ReturnAdviceViews is the read side of the ReturnAdviceView local read
// model: what the completion hook and the poller consult.
type ReturnAdviceViews interface {
	// Get returns the advice, or ok=false when the poller never saw it (a
	// non-network receipt: the completion hook does nothing).
	Get(ctx context.Context, authorizationID string) (views.ReturnAdvice, bool, error)
	// PendingOutcomes returns every outcome awaiting submission (PENDING) or
	// settlement (SUBMITTED, its transaction not terminal), oldest first;
	// the drain's work list. SUCCEEDED and FAILED are terminal and never
	// listed.
	PendingOutcomes(ctx context.Context) ([]views.PendingReturnOutcome, error)
	// LatestReadyAt returns the newest readyAt the copy holds (the poll
	// watermark), or ok=false when the copy is empty.
	LatestReadyAt(ctx context.Context) (time.Time, bool, error)
}

// NetworkReturnsGateway is the outbound seam to the retail network's Vendor
// API (ADR 0012): poll the advices of our nodes, report the graded outcome.
// The adapter under this port is the only code that may name the network's
// vocabulary.
type NetworkReturnsGateway interface {
	// PollAdvices returns one poll's advices, paging internally. Re-delivery
	// is normal and harmless (the feed upsert is idempotent).
	PollAdvices(ctx context.Context, readyAfter time.Time) ([]views.ReturnAdvice, error)
	// SubmitReturnOutcome reports the graded result of one advice. On success
	// it returns the network's transaction id (the 202). A definitive
	// refusal wraps ErrOutcomeRefused; anything else is retriable.
	SubmitReturnOutcome(ctx context.Context, out views.ReturnOutcome) (transactionID string, err error)
	// OutcomeTransaction reads the network's verdict of a submission
	// (PROCESSING, SUCCEEDED or FAILED).
	OutcomeTransaction(ctx context.Context, transactionID string) (views.ReturnOutcomeTransaction, error)
}
