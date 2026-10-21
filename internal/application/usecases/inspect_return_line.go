package usecases

import (
	"context"
	"strconv"
	"strings"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
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
	return d, nil
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
