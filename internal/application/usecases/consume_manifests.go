package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// ApplyManifest feeds the package-manifest local copy (ADR 0003) from
// fulfillment-execution's PackageManifested facts, REVERSING the WES
// work-unit formula its order_ref carries: `{order_id}-line-{line_no}`
// splits back into the order id and the 1-based line number, so the copy is
// keyed exactly where AuthorizeReturn looks a return up.
type ApplyManifest struct {
	UnitOfWork ports.UnitOfWork
	Processed  ports.ProcessedEvents
	Units      ports.ManifestedUnitFeed
}

// Handle applies one manifested work unit. orderRef is the fact's
// order_ref; manifestedAt is the fact's occurred-at (the CloudEvent `time`
// attribute — the payload itself carries no timestamp).
func (uc *ApplyManifest) Handle(ctx context.Context, eventID, orderRef, packageID string, manifestedAt time.Time) (ConsumeOutcome, error) {
	ref, err := shared.ParseWorkUnit(orderRef)
	if err != nil {
		return "", invalid(err)
	}
	if packageID == "" {
		return "", invalidf("manifest of %s carries no package id", orderRef)
	}
	if manifestedAt.IsZero() {
		return "", invalidf("manifest of %s carries no occurred-at", orderRef)
	}
	return claimAndApply(ctx, uc.UnitOfWork, uc.Processed, eventID, func(ctx context.Context) error {
		if err := uc.Units.ApplyManifestedUnit(ctx, ref.OrderID().String(), ref.LineNo(), views.ManifestedUnit{
			PackageID:    packageID,
			ManifestedAt: manifestedAt,
		}); err != nil {
			return fmt.Errorf("apply manifest of %s: %w", orderRef, err)
		}
		return nil
	})
}
