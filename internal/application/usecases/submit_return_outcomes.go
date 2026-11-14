package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// SubmitReturnOutcomes is the drain half of ADR 0012: every stored outcome
// that is PENDING (not yet accepted) or SUBMITTED (accepted, its transaction
// not terminal) makes one step forward per tick:
//
//   - PENDING: submit to the network. A 202 records the transaction id
//     (SUBMITTED); a definitive refusal wraps ports.ErrOutcomeRefused and is
//     recorded FAILED, terminal; anything else leaves it PENDING for the
//     next tick.
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
}

// OutcomeDrainResult reports what one drain tick did, for the worker's log
// line.
type OutcomeDrainResult struct {
	Submitted int
	Settled   int
	Failed    int
}

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
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return res, firstErr
}

func (uc *SubmitReturnOutcomes) step(ctx context.Context, p views.PendingReturnOutcome) (outcomeDrainStep, error) {
	if p.TransactionID == "" {
		return uc.submit(ctx, p.Outcome)
	}
	return uc.settle(ctx, p.Outcome.AuthorizationID, p.TransactionID)
}

// submit makes the one write of an outcome that has no transaction yet.
func (uc *SubmitReturnOutcomes) submit(ctx context.Context, out views.ReturnOutcome) (outcomeDrainStep, error) {
	id := out.AuthorizationID
	txID, err := uc.Gateway.SubmitReturnOutcome(ctx, out)
	if err != nil {
		if errors.Is(err, ports.ErrOutcomeRefused) {
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
}
