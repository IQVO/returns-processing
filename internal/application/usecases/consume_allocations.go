package usecases

import (
	"context"
	"fmt"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// ApplyAllocations feeds the allocations local copy (ADR 0003) from
// order-management's OrderAllocated / OrderPartiallyAllocated facts. Every
// allocated line of one fact is applied under the SAME claim of the
// CloudEvents id: the event is the unit of work, not the line.
type ApplyAllocations struct {
	UnitOfWork ports.UnitOfWork
	Processed  ports.ProcessedEvents
	Lines      ports.AllocatedLineFeed
}

// AllocatedLineIn is one allocated line of an allocation fact (the sources
// carry NO quantity — a line is identified by its number and its sku).
type AllocatedLineIn struct {
	LineNo int32
	SKU    string
}

// Handle applies every allocated line of one allocation fact.
func (uc *ApplyAllocations) Handle(ctx context.Context, eventID, orderID string, lines []AllocatedLineIn) (ConsumeOutcome, error) {
	id, err := shared.NewOrderID(orderID)
	if err != nil {
		return "", invalid(err)
	}
	if len(lines) == 0 {
		return "", invalidf("allocation of %s carries no lines", orderID)
	}
	for _, l := range lines {
		if _, err := shared.NewOrderLineRef(id, l.LineNo); err != nil {
			return "", invalid(err)
		}
		if _, err := shared.NewSKU(l.SKU); err != nil {
			return "", invalid(err)
		}
	}
	return claimAndApply(ctx, uc.UnitOfWork, uc.Processed, eventID, func(ctx context.Context) error {
		for _, l := range lines {
			if err := uc.Lines.ApplyAllocatedLine(ctx, orderID, l.LineNo, l.SKU); err != nil {
				return fmt.Errorf("apply allocated line %s/%d: %w", orderID, l.LineNo, err)
			}
		}
		return nil
	})
}
