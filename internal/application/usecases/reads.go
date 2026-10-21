package usecases

import (
	"context"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
)

// GetReturn serves one RMA by id (ports.ErrNotFound -> HTTP 404).
type GetReturn struct {
	RMAs ports.ReturnAuthorizationRepo
}

// Execute returns the return view.
func (uc *GetReturn) Execute(ctx context.Context, id string) (views.ReturnView, error) {
	r, err := uc.RMAs.FindByID(ctx, id)
	if err != nil {
		return views.ReturnView{}, err
	}
	return ReturnView(r), nil
}

// ListReturnsByOrder serves every RMA of one order, by line number then id.
type ListReturnsByOrder struct {
	RMAs ports.ReturnAuthorizationRepo
}

// Execute returns the order's return views.
func (uc *ListReturnsByOrder) Execute(ctx context.Context, orderID string) ([]views.ReturnView, error) {
	list, err := uc.RMAs.ListByOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	out := make([]views.ReturnView, 0, len(list))
	for _, r := range list {
		out = append(out, ReturnView(r))
	}
	return out, nil
}

// GetDispositionPolicy serves the policy table: the CURRENT version, or any
// declared version by number (ports.ErrNotFound -> HTTP 404).
type GetDispositionPolicy struct {
	Policies ports.DispositionPolicyRepo
}

// Execute returns the current policy version.
func (uc *GetDispositionPolicy) Execute(ctx context.Context) (views.PolicyView, error) {
	p, err := uc.Policies.Current(ctx)
	if err != nil {
		return views.PolicyView{}, err
	}
	return PolicyView(p), nil
}

// Version returns one declared version by number.
func (uc *GetDispositionPolicy) Version(ctx context.Context, version int64) (views.PolicyView, error) {
	p, err := uc.Policies.FindByVersion(ctx, version)
	if err != nil {
		return views.PolicyView{}, err
	}
	return PolicyView(p), nil
}

// ReturnView projects an RMA aggregate into its read DTO — the one place
// the wire shape is derived from the aggregate.
func ReturnView(r *returnauthorization.RMA) views.ReturnView {
	line := r.Line()
	return views.ReturnView{
		ID:            r.ID(),
		OrderID:       line.OrderID().String(),
		LineNo:        line.LineNo(),
		WorkUnit:      line.WorkUnit(),
		SKU:           r.SKU(),
		Quantity:      r.Quantity(),
		SiteID:        r.SiteID(),
		ReasonCode:    r.ReasonCode(),
		Status:        string(r.Status()),
		RejectionCode: string(r.RejectionCode()),
		WindowEndsAt:  r.WindowEndsAt(),
		Version:       r.Version(),
	}
}

// PolicyView projects a policy version into its read DTO (the scalar bounds;
// the rule table has no domain accessor).
func PolicyView(p *disposition.DispositionPolicy) views.PolicyView {
	return views.PolicyView{
		Version:          p.Version(),
		ReturnWindowDays: p.ReturnWindowDays(),
		MaxUnitsPerLine:  p.MaxUnitsPerLine(),
		VetoOutcome:      string(p.VetoOutcome()),
		DeclaredAt:       p.DeclaredAt(),
	}
}
