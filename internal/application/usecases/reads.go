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

// ListReturnsByOrder serves the RMAs of one order, by line number then id: one
// keyset page at a time (Page, behind GET /returns) or all of them (Execute,
// behind the MCP tool and the A2A skill, which return complete data).
type ListReturnsByOrder struct {
	RMAs ports.ReturnAuthorizationRepo
}

// walkPageSize is how many rows Execute asks the repository for per round trip.
const walkPageSize = 500

// Page returns at most limit return views strictly after the keyset position
// after (the first page when nil).
func (uc *ListReturnsByOrder) Page(ctx context.Context, orderID string, after *views.ReturnKey, limit int) ([]views.ReturnView, error) {
	list, err := uc.RMAs.ListByOrder(ctx, orderID, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]views.ReturnView, 0, len(list))
	for _, r := range list {
		out = append(out, ReturnView(r))
	}
	return out, nil
}

// Execute returns every return view of the order, walking the pages.
func (uc *ListReturnsByOrder) Execute(ctx context.Context, orderID string) ([]views.ReturnView, error) {
	out := []views.ReturnView{}
	var after *views.ReturnKey
	for {
		page, err := uc.Page(ctx, orderID, after, walkPageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < walkPageSize {
			return out, nil
		}
		last := page[len(page)-1]
		after = &views.ReturnKey{LineNo: last.LineNo, ID: last.ID}
	}
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

// PolicyView projects a policy version into its read DTO (the scalar bounds
// and the rule table).
func PolicyView(p *disposition.DispositionPolicy) views.PolicyView {
	return views.PolicyView{
		Version:          p.Version(),
		ReturnWindowDays: p.ReturnWindowDays(),
		MaxUnitsPerLine:  p.MaxUnitsPerLine(),
		VetoOutcome:      string(p.VetoOutcome()),
		Rules:            p.Rules(),
		DeclaredAt:       p.DeclaredAt(),
	}
}
