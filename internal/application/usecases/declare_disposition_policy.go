package usecases

import (
	"context"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
)

// PolicyDeclaration declares one append-only policy version (ADR 0002: a
// change is a new version, never a rewrite): the return window and quantity
// cap the RMA rule uses, the veto outcome a guardrail demotion falls back
// to, and the total grade x product-class rule table.
type PolicyDeclaration struct {
	Version          int64
	ReturnWindowDays int32
	MaxUnitsPerLine  int64
	VetoOutcome      disposition.Outcome // "" defaults to DISPOSE
	Rules            []disposition.Rule
}

// DeclareDispositionPolicy validates and installs a new policy version and
// marks it current. The domain holds no declaration fact (ADR 0004: the
// policy is internal state, not a published event), so the use case saves
// and nothing else.
type DeclareDispositionPolicy struct {
	Policies   ports.DispositionPolicyRepo
	Clock      ports.Clock
	UnitOfWork ports.UnitOfWork // nil = unwrapped
}

// Execute returns the persisted version.
func (uc *DeclareDispositionPolicy) Execute(ctx context.Context, in PolicyDeclaration) (*disposition.DispositionPolicy, error) {
	p, err := disposition.Declare(disposition.PolicyInput{
		Version:          in.Version,
		ReturnWindowDays: in.ReturnWindowDays,
		MaxUnitsPerLine:  in.MaxUnitsPerLine,
		VetoOutcome:      in.VetoOutcome,
		Rules:            in.Rules,
		DeclaredAt:       uc.Clock.Now(),
	})
	if err != nil {
		return nil, err
	}
	if err := atomically(ctx, uc.UnitOfWork, func(ctx context.Context) error {
		return uc.Policies.Save(ctx, p)
	}); err != nil {
		return nil, err
	}
	return p, nil
}
