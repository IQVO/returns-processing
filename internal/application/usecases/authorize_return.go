package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
)

// ErrOrderLineNotAllocated is the fail-closed outcome when the allocations
// local copy holds no entry for the requested order line (ADR 0003): a line
// the copy does not know cannot be returned. The HTTP adapter maps it to 404.
var ErrOrderLineNotAllocated = errors.New("usecases: order line is not allocated")

// ReturnRequest is the operator input of a return authorization: which
// shipped order line is coming back, how many units, to which site, and why.
// The sku is NOT taken from the caller — the allocations copy is the truth.
type ReturnRequest struct {
	OrderID    string
	LineNo     int32
	Quantity   int64
	SiteID     string
	ReasonCode string
}

// AuthorizeReturn composes the invariants of ADR 0002 into one decision:
// the allocations copy supplies the authoritative sku, the manifested copy
// and the one-open-RMA query supply the authorization facts, the current
// policy supplies the window and the cap, and the pure domain constructor
// decides. A refused request returns the typed domain error
// (ErrOpenRMAExists, ErrLineNotManifested, ErrWindowExpired,
// ErrQuantityExceedsPolicy) — the HTTP edge maps it with
// returnauthorization.RejectionCodeFor. Nothing is persisted on refusal:
// RejectReturn is not a use case.
type AuthorizeReturn struct {
	RMAs       ports.ReturnAuthorizationRepo
	Policies   ports.DispositionPolicyRepo
	Allocated  ports.AllocatedLines
	Manifested ports.ManifestedUnits
	Events     ports.EventPublisher
	IDs        ports.IDGen
	Clock      ports.Clock
	UnitOfWork ports.UnitOfWork // nil = unwrapped
}

// Execute authorizes the return and returns the decided RMA (AUTHORIZED,
// version 1, ReturnAuthorized published in the same unit as the save).
func (uc *AuthorizeReturn) Execute(ctx context.Context, in ReturnRequest) (*returnauthorization.RMA, error) {
	policy, err := uc.Policies.Current(ctx)
	if err != nil {
		return nil, err
	}
	alloc, ok := uc.Allocated.Get(in.OrderID, in.LineNo)
	if !ok {
		return nil, fmt.Errorf("%w: order %s line %d", ErrOrderLineNotAllocated, in.OrderID, in.LineNo)
	}
	now := uc.Clock.Now()
	rma, err := returnauthorization.Request(returnauthorization.RequestInput{
		RMAID:      uc.IDs.New("rma"),
		OrderID:    in.OrderID,
		LineNo:     in.LineNo,
		SKU:        alloc.SKU,
		Quantity:   in.Quantity,
		SiteID:     in.SiteID,
		ReasonCode: in.ReasonCode,
	})
	if err != nil {
		return nil, err
	}
	facts := returnauthorization.Facts{
		PolicyMaxUnits: policy.MaxUnitsPerLine(),
		WindowEndsAt:   policy.WindowEndsFrom(now),
		Now:            now,
	}
	if _, manifested := uc.Manifested.GetByOrderLine(in.OrderID, in.LineNo); manifested {
		// The manifested copy holds a work unit for this line; the RMA's own
		// line key is the work-unit id the domain compares against, so its
		// presence in the copy is the authorization fact.
		facts.ManifestedWorkUnit = rma.Line().WorkUnit()
	}
	if open, err := uc.RMAs.FindOpenByOrderLine(ctx, in.OrderID, in.LineNo); err == nil {
		facts.ExistingOpenRMAID = open.ID()
	} else if !errors.Is(err, ports.ErrNotFound) {
		return nil, err
	}
	if err := rma.Authorize(facts); err != nil {
		return nil, err
	}
	if err := atomically(ctx, uc.UnitOfWork, func(ctx context.Context) error {
		if err := uc.RMAs.Save(ctx, rma); err != nil {
			return err
		}
		return uc.Events.PublishRMAEvents(ctx, rma.DomainEvents()...)
	}); err != nil {
		return nil, err
	}
	return rma, nil
}
