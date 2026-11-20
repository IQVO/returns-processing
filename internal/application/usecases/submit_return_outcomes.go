package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// DefaultReleasedRefusalWindow bounds the retry of a refusal the network releases
// (ADR 0013): from the instant the outcome was composed (PendingSince), a released
// refusal keeps the outcome PENDING; past it the refusal is recorded terminal, exactly
// like a final one. A day covers an event-fed fact that arrives late and a suspended
// vendor that an operator reinstates within a working day.
const DefaultReleasedRefusalWindow = 24 * time.Hour

// SubmitReturnOutcomes is the drain half of ADR 0012: every stored outcome
// that is PENDING (not yet accepted) or SUBMITTED (accepted, its transaction
// not terminal) makes one step forward per tick:
//
//   - PENDING: submit to the network. A 202 records the transaction id
//     (SUBMITTED); a final refusal (ports.ErrOutcomeRefused) is recorded
//     FAILED, terminal; a refusal the network RELEASES (a ports.OutcomeRefusal
//     with Released, ADR 0013) leaves it PENDING with the last refusal noted, until
//     RetryWindow is spent and it is recorded FAILED like a final one; anything else
//     leaves it PENDING for the next tick.
//   - SUBMITTED: read the transaction. SUCCEEDED is the happy end (the
//     outcome stays SUBMITTED, terminal); FAILED is recorded FAILED with the
//     network's errors; PROCESSING is left for the next tick.
//
// One outcome's failure never stops the drain: the tick reports the first
// error but has already stepped every other outcome it could.
type SubmitReturnOutcomes struct {
	Gateway ports.NetworkReturnsGateway
	Views   ports.ReturnAdviceViews
	Feed    ports.ReturnAdviceFeed
	// Clock reads the time of the retry window; nil reads the system clock.
	Clock ports.Clock
	// RetryWindow bounds the retry of released refusals; zero is
	// DefaultReleasedRefusalWindow.
	RetryWindow time.Duration
}

// OutcomeDrainResult reports what one drain tick did, for the worker's log
// line.
type OutcomeDrainResult struct {
	Submitted int
	Settled   int
	Failed    int
	// Released lists the refusals the network released this tick: the outcome stays
	// PENDING and the next tick submits it again under the same key.
	Released []ReleasedRefusal
}

// ReleasedRefusal is one refusal the network released: which outcome, which problem slug.
type ReleasedRefusal struct{ AuthorizationID, Slug string }

// Execute drains the pending outcomes once.
func (uc *SubmitReturnOutcomes) Execute(ctx context.Context) (OutcomeDrainResult, error) {
	pending, err := uc.Views.PendingOutcomes(ctx)
	if err != nil {
		return OutcomeDrainResult{}, err
	}
	var res OutcomeDrainResult
	var firstErr error
	for _, p := range pending {
		step, err := uc.step(ctx, p)
		res.Submitted += step.submitted
		res.Settled += step.settled
		res.Failed += step.failed
		if step.released != nil {
			res.Released = append(res.Released, *step.released)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return res, firstErr
}

func (uc *SubmitReturnOutcomes) step(ctx context.Context, p views.PendingReturnOutcome) (outcomeDrainStep, error) {
	if p.TransactionID == "" {
		return uc.submit(ctx, p)
	}
	return uc.settle(ctx, p.Outcome.AuthorizationID, p.TransactionID)
}

// submit makes the one write of an outcome that has no transaction yet.
func (uc *SubmitReturnOutcomes) submit(ctx context.Context, p views.PendingReturnOutcome) (outcomeDrainStep, error) {
	id := p.Outcome.AuthorizationID
	txID, err := uc.Gateway.SubmitReturnOutcome(ctx, p.Outcome)
	if err != nil {
		var refusal ports.OutcomeRefusal
		if errors.As(err, &refusal) && refusal.Released() && !uc.windowSpent(p.PendingSince) {
			return uc.keepPending(ctx, id, refusal, err)
		}
		if errors.Is(err, ports.ErrOutcomeRefused) || refusal != nil {
			if rerr := uc.Feed.MarkOutcomeFailed(ctx, id, err.Error()); rerr != nil {
				return outcomeDrainStep{}, rerr
			}
			return outcomeDrainStep{failed: 1}, nil
		}
		// Not refused, just not heard: the next tick retries with the
		// same idempotency key, so this is a warning, not a verdict.
		return outcomeDrainStep{}, fmt.Errorf("submit outcome %s: %w", id, err)
	}
	if err := uc.Feed.MarkOutcomeSubmitted(ctx, id, txID); err != nil {
		return outcomeDrainStep{}, err
	}
	return outcomeDrainStep{submitted: 1}, nil
}

// keepPending leaves an outcome whose refusal the network released PENDING and notes
// the refusal for the operator; the next tick submits it again, same body, same key.
func (uc *SubmitReturnOutcomes) keepPending(ctx context.Context, id string, refusal ports.OutcomeRefusal, err error) (outcomeDrainStep, error) {
	note := "released by the network, retried under the same key: " + err.Error()
	if nerr := uc.Feed.NoteOutcomeRefusal(ctx, id, note); nerr != nil {
		return outcomeDrainStep{}, nerr
	}
	return outcomeDrainStep{released: &ReleasedRefusal{AuthorizationID: id, Slug: refusal.ProblemSlug()}}, nil
}

// windowSpent reports whether the retry window of an outcome composed at since is over.
func (uc *SubmitReturnOutcomes) windowSpent(since time.Time) bool {
	window := uc.RetryWindow
	if window <= 0 {
		window = DefaultReleasedRefusalWindow
	}
	now := time.Now()
	if uc.Clock != nil {
		now = uc.Clock.Now()
	}
	return now.Sub(since) > window
}

// settle reads the transaction of an accepted submission to its verdict.
func (uc *SubmitReturnOutcomes) settle(ctx context.Context, id, transactionID string) (outcomeDrainStep, error) {
	tx, err := uc.Gateway.OutcomeTransaction(ctx, transactionID)
	if err != nil {
		return outcomeDrainStep{}, fmt.Errorf("settle outcome %s: %w", id, err)
	}
	switch tx.Status {
	case views.TransactionSucceeded:
		if err := uc.Feed.MarkOutcomeSucceeded(ctx, id); err != nil {
			return outcomeDrainStep{}, err
		}
		return outcomeDrainStep{settled: 1}, nil
	case views.TransactionFailed:
		if rerr := uc.Feed.MarkOutcomeFailed(ctx, id, tx.Errors); rerr != nil {
			return outcomeDrainStep{}, rerr
		}
		return outcomeDrainStep{failed: 1}, nil
	default: // PROCESSING: the next tick settles it.
		return outcomeDrainStep{}, nil
	}
}

type outcomeDrainStep struct {
	submitted int
	settled   int
	failed    int
	released  *ReleasedRefusal
}
