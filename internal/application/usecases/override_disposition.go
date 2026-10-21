package usecases

import (
	"context"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
)

// Override is the operator input: the disposition being superseded, the new
// outcome, and the mandatory explanation.
type Override struct {
	DispositionID string
	Outcome       disposition.Outcome
	Reason        string
}

// OverrideDisposition supersedes a decision with an explained operator
// override (ADR 0002): a NEW immutable Disposition carrying Supersedes and
// the reason; the original is never mutated. A RESTOCK override stages the
// line like any restock would (ReturnLineStaged, idempotent downstream on
// the new disposition id).
type OverrideDisposition struct {
	Dispositions ports.DispositionRepo
	Events       ports.EventPublisher
	IDs          ports.IDGen
	Clock        ports.Clock
	UnitOfWork   ports.UnitOfWork // nil = unwrapped
}

// Execute applies the override and returns the superseding disposition.
func (uc *OverrideDisposition) Execute(ctx context.Context, in Override) (*disposition.Disposition, error) {
	orig, err := uc.Dispositions.FindByID(ctx, in.DispositionID)
	if err != nil {
		return nil, err
	}
	over, err := orig.Override(disposition.OverrideInput{
		NewDispositionID: uc.IDs.New("disp"),
		Outcome:          in.Outcome,
		Reason:           in.Reason,
		DecidedAt:        uc.Clock.Now(),
	})
	if err != nil {
		return nil, err
	}
	if over.Outcome() == disposition.OutcomeRestock {
		// The domain raises staging on demand; a fresh disposition stages
		// exactly once, so this cannot fail.
		if err := over.Stage(); err != nil {
			return nil, err
		}
	}
	if err := atomically(ctx, uc.UnitOfWork, func(ctx context.Context) error {
		if err := uc.Dispositions.Save(ctx, over); err != nil {
			return err
		}
		return uc.Events.PublishDispositionEvents(ctx, over.DomainEvents()...)
	}); err != nil {
		return nil, err
	}
	return over, nil
}
