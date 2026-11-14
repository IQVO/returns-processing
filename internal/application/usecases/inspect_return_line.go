package usecases

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// Inspection is the inspector input: the grade of one received line, how
// many of the received units the grade speaks for, and why.
type Inspection struct {
	ReceiptID   string
	LineNo      int32
	Grade       shared.Grade
	Quantity    int64
	ReasonCodes []string
}

// InspectReturnLine grades one received line and decides its disposition in
// one atomic unit (ADR 0002): the receipt records the grade
// (ReturnLineInspected), the pure policy function decides the outcome from
// the grade, the product-classification copy and the current policy version
// (DispositionDecided, plus ReturnLineStaged when the outcome is RESTOCK),
// and both aggregates save and publish together or not at all.
type InspectReturnLine struct {
	Receipts     ports.ReturnReceiptRepo
	Dispositions ports.DispositionRepo
	Policies     ports.DispositionPolicyRepo
	Products     ports.ProductClasses
	Events       ports.EventPublisher
	IDs          ports.IDGen
	Clock        ports.Clock
	UnitOfWork   ports.UnitOfWork // nil = unwrapped
	// Advices and AdviceFeed are the network return-advice seam of ADR 0012.
	// Both nil (tests, BDD, the non-network flows) means the completion hook
	// is off: inspecting a non-network receipt is unchanged.
	Advices    ports.ReturnAdviceViews
	AdviceFeed ports.ReturnAdviceFeed
}

// Execute inspects the line and returns the decision.
func (uc *InspectReturnLine) Execute(ctx context.Context, in Inspection) (*disposition.Disposition, error) {
	r, err := uc.Receipts.FindByID(ctx, in.ReceiptID)
	if err != nil {
		return nil, err
	}
	policy, err := uc.Policies.Current(ctx)
	if err != nil {
		return nil, err
	}
	now := uc.Clock.Now()
	if err := r.Inspect(in.LineNo, in.Grade, in.Quantity, in.ReasonCodes, now); err != nil {
		return nil, err
	}
	line, err := r.Line(in.LineNo)
	if err != nil {
		return nil, err
	}
	pc, known := uc.Products.Get(line.SKU())
	d, err := disposition.DecideForLine(disposition.LineDecision{
		DispositionID:  uc.IDs.New("disp"),
		ReceiptID:      in.ReceiptID,
		LineNo:         in.LineNo,
		SKU:            line.SKU(),
		Quantity:       line.InspectedQuantity(),
		Grade:          line.Grade(),
		Classification: classification(line.SKU(), pc, known),
		Policy:         policy,
		SiteID:         r.SiteID(),
		RMAID:          line.RMAID(),
		ReceivedAt:     r.OpenedAt(),
		DecidedAt:      now,
	})
	if err != nil {
		return nil, err
	}
	if err := atomically(ctx, uc.UnitOfWork, func(ctx context.Context) error {
		if err := uc.Receipts.Save(ctx, r); err != nil {
			return err
		}
		if err := uc.Dispositions.Save(ctx, d); err != nil {
			return err
		}
		if err := uc.Events.PublishReceiptEvents(ctx, r.DomainEvents()...); err != nil {
			return err
		}
		return uc.Events.PublishDispositionEvents(ctx, d.DomainEvents()...)
	}); err != nil {
		return nil, err
	}
	// ADR 0012: a receipt opened against a network advice (its rmaId IS the
	// advice's authorizationId) composes and stores the advice's outcome the
	// moment its last line is graded — AFTER the unit of work, so no network
	// concern ever sits inside it. The submit drain does the HTTP.
	if err := uc.completeAdvice(ctx, r); err != nil {
		return nil, err
	}
	return d, nil
}

// completeAdvice stores the composed outcome when r is the FIRST fully
// inspected receipt of a network advice (ADR 0012 Decision 5). A receipt of
// no advice (the non-network flows), an incomplete one, or an advice whose
// outcome already exists does nothing. The multi-parcel gap is recorded in
// the ADR: the first completed receipt wins, once.
func (uc *InspectReturnLine) completeAdvice(ctx context.Context, r *returnreceipt.Receipt) error {
	if uc.Advices == nil || uc.AdviceFeed == nil || !fullyInspected(r) {
		return nil
	}
	authorizationID := r.RMAID()
	advice, ok, err := uc.Advices.Get(ctx, authorizationID)
	if err != nil || !ok || advice.OutcomeState != "" {
		return err
	}
	outcome, err := uc.composeOutcome(ctx, advice, r)
	if err != nil {
		return err
	}
	err = uc.AdviceFeed.MarkOutcomePending(ctx, authorizationID, outcome)
	if errors.Is(err, ports.ErrOutcomeAlreadyStored) {
		return nil // a sibling receipt won the race; the once-rule holds
	}
	return err
}

// composeOutcome folds the receipt's latest disposition decisions into the
// network's outcome body: RESTOCK units are restockable, every other outcome
// unsellable; the ownerRef of a line the advice authorized rides its
// matching advice line (by catalog item); a sku the advice never authorized
// is submitted anyway — the network records it unexpected, and goods on the
// dock are never turned away.
func (uc *InspectReturnLine) composeOutcome(ctx context.Context, advice views.ReturnAdvice, r *returnreceipt.Receipt) (views.ReturnOutcome, error) {
	owners := map[string]string{}
	for _, l := range advice.Lines {
		owners[l.CatalogItemID] = l.OwnerRef
	}
	out := views.ReturnOutcome{AuthorizationID: advice.AuthorizationID, NodeID: advice.NodeID, ReceivedAt: r.OpenedAt()}
	index := map[string]int{}
	for _, line := range r.Lines() {
		d, err := uc.Dispositions.FindByReceiptLine(ctx, r.ID(), line.LineNo())
		if err != nil {
			return views.ReturnOutcome{}, err
		}
		sku := line.SKU()
		key := sku + "\x00" + owners[sku]
		i, known := index[key]
		if !known {
			out.Lines = append(out.Lines, views.ReturnOutcomeLine{CatalogItemID: sku, OwnerRef: owners[sku]})
			i = len(out.Lines) - 1
			index[key] = i
		}
		if d.Outcome() == disposition.OutcomeRestock {
			out.Lines[i].QtyRestockable += d.Quantity()
		} else {
			out.Lines[i].QtyUnsellable += d.Quantity()
		}
	}
	return out, nil
}

// fullyInspected reports whether every line of the receipt carries a grade.
func fullyInspected(r *returnreceipt.Receipt) bool {
	if len(r.Lines()) == 0 {
		return false
	}
	for _, l := range r.Lines() {
		if !l.Inspected() {
			return false
		}
	}
	return true
}

// classification derives the domain Classification from the product-master
// local copy, using the tag grammar views.ProductClass documents. A SKU the
// copy does not hold stays unclassified (fail-open for grading: the "*"
// default rule catches it, the hazmat guardrail applies once known).
func classification(sku string, pc views.ProductClass, known bool) disposition.Classification {
	c := disposition.Classification{SKU: sku, TemperatureClass: pc.TemperatureClass, Classified: known}
	if !known {
		return c
	}
	for _, tag := range pc.Tags {
		switch {
		case tag == "hazmat":
			c.Hazmat = true
		case strings.HasPrefix(tag, "class:"):
			c.ProductClass = strings.TrimPrefix(tag, "class:")
		case strings.HasPrefix(tag, "dot:"):
			if n, err := strconv.Atoi(strings.TrimPrefix(tag, "dot:")); err == nil {
				c.DoTHazardClass = n
			}
		case strings.HasPrefix(tag, "force:"):
			c.Forces = disposition.Outcome(strings.TrimPrefix(tag, "force:"))
		}
	}
	return c
}
