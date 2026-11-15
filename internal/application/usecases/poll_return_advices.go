package usecases

import (
	"context"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// advicePollOverlap is how far before the watermark a poll looks (the same
// rule the purchase-order integration uses): an advice committed on the
// network just after the previous poll began is still found, and the
// re-delivery that follows is harmless — the feed upsert is idempotent.
const advicePollOverlap = 2 * time.Minute

// PollReturnAdvices is the poll half of ADR 0012: fetch the network's
// advices for our nodes and upsert them into the ReturnAdviceView copy.
// First sight and re-delivery take the same path; the view's outcome columns
// are never touched here.
type PollReturnAdvices struct {
	Gateway ports.NetworkReturnsGateway
	Views   ports.ReturnAdviceViews
	Feed    ports.ReturnAdviceFeed
}

// AdvicePollResult reports what one tick did, for the worker's log line.
type AdvicePollResult struct {
	// Seen is the number of advices the network handed us this tick
	// (duplicates inside one poll counted once).
	Seen int
	// Applied is the number of upserts committed (a re-delivery applies too).
	Applied int
}

// Execute runs one poll. A gateway or store failure fails the tick (the
// worker logs it and the next tick retries); it never returns a partial
// write: each upsert is its own unit, and an upsert that fails stops the
// tick so the watermark does not advance past it.
func (uc *PollReturnAdvices) Execute(ctx context.Context) (AdvicePollResult, error) {
	since, err := uc.windowStart(ctx)
	if err != nil {
		return AdvicePollResult{}, err
	}
	advices, err := uc.Gateway.PollAdvices(ctx, since)
	if err != nil {
		return AdvicePollResult{}, err
	}
	res := AdvicePollResult{Seen: countUnique(advices)}
	for _, a := range advices {
		if err := uc.Feed.ApplyReturnAdvice(ctx, a); err != nil {
			return res, err
		}
		res.Applied++
	}
	return res, nil
}

// windowStart is the watermark minus the overlap; the zero time (poll from
// the beginning) when the copy is empty.
func (uc *PollReturnAdvices) windowStart(ctx context.Context) (time.Time, error) {
	latest, ok, err := uc.Views.LatestReadyAt(ctx)
	if err != nil || !ok {
		return time.Time{}, err
	}
	return latest.Add(-advicePollOverlap), nil
}

// countUnique counts distinct authorizationIds: the network pages oldest
// first and an advice can appear twice inside one poll's pages.
func countUnique(advices []views.ReturnAdvice) int {
	seen := make(map[string]bool, len(advices))
	for _, a := range advices {
		seen[a.AuthorizationID] = true
	}
	return len(seen)
}
