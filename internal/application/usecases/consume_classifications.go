package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// ErrInvalidEvent marks a consumed event that can NEVER be applied — an
// empty sku, a work unit that is not the `{order_id}-line-{line_no}`
// formula, a classification without a version. It is deterministic: the
// consumer dead-letters such an event at once instead of retrying it.
var ErrInvalidEvent = errors.New("usecases: invalid event")

// ConsumeOutcome reports what a consumer use case did with one CloudEvent.
type ConsumeOutcome string

// The outcomes.
const (
	// OutcomeApplied: the local copy was updated.
	OutcomeApplied ConsumeOutcome = "applied"
	// OutcomeDuplicate: this CloudEvents id was already processed; nothing
	// changed (the id-dedupe rule: same id twice = one effect).
	OutcomeDuplicate ConsumeOutcome = "duplicate"
)

// invalid wraps a deterministic payload problem in ErrInvalidEvent.
func invalid(err error) error { return fmt.Errorf("%w: %w", ErrInvalidEvent, err) }

// invalidf is invalid with a formatted message.
func invalidf(format string, args ...any) error { return invalid(fmt.Errorf(format, args...)) }

// claimAndApply is the skeleton every consumer use case shares: the claim of
// the CloudEvents id and the effect run in ONE unit of work (a nil
// UnitOfWork runs them unwrapped, the memory wiring), so a rollback
// un-claims the id and a redelivery is processed, not skipped — the fleet's
// dedupe rule.
func claimAndApply(ctx context.Context, uow ports.UnitOfWork, processed ports.ProcessedEvents,
	eventID string, effect func(ctx context.Context) error,
) (ConsumeOutcome, error) {
	if eventID == "" {
		return "", invalid(errors.New("event id is required"))
	}
	outcome := OutcomeApplied
	err := atomically(ctx, uow, func(ctx context.Context) error {
		claimed, err := processed.Claim(ctx, eventID)
		if err != nil {
			return fmt.Errorf("claim processed event: %w", err)
		}
		if !claimed {
			outcome = OutcomeDuplicate
			return nil
		}
		return effect(ctx)
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

// ApplyProductClassification feeds the classification local copy (ADR 0003)
// from product-master's ProductClassified facts. A fact whose version is not
// newer than the stored one changes nothing AT THE STORE (the LOCAL COPY
// RULE); its claim still commits, so the replay of the SAME id is the
// duplicate it deserves to be.
type ApplyProductClassification struct {
	UnitOfWork ports.UnitOfWork
	Processed  ports.ProcessedEvents
	Classes    ports.ProductClassFeed
}

// Handle applies one classification fact.
func (uc *ApplyProductClassification) Handle(ctx context.Context, eventID, sku string, pc views.ProductClass) (ConsumeOutcome, error) {
	if _, err := shared.NewSKU(sku); err != nil {
		return "", invalid(err)
	}
	if pc.Version < 1 {
		return "", invalidf("classification of %s carries version %d, want >= 1", sku, pc.Version)
	}
	return claimAndApply(ctx, uc.UnitOfWork, uc.Processed, eventID, func(ctx context.Context) error {
		if err := uc.Classes.ApplyProductClass(ctx, sku, pc); err != nil {
			return fmt.Errorf("apply classification of %s: %w", sku, err)
		}
		return nil
	})
}
