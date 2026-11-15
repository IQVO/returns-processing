package retailnetwork

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// ReturnsConfig is what NewLiveReturnsGateway needs (ADR 0012): the base
// URL of the network's Vendor API, the vendor this service operates as, and
// the HTTP client (nil = a default with a 30s timeout).
type ReturnsConfig struct {
	BaseURL    string
	VendorID   string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// LiveReturnsGateway is the return seam of ports.NetworkReturnsGateway
// against the network's Vendor API (ADR 0012), on the pattern of the
// purchase-order integration's demand loop: our vocabulary on the outside,
// the network's wire shapes mapped at this edge and nowhere else.
type LiveReturnsGateway struct {
	client *returnsClient
	logger *slog.Logger
}

var _ ports.NetworkReturnsGateway = (*LiveReturnsGateway)(nil)

// NewLiveReturnsGateway builds the gateway; a missing base URL or vendor id
// is a wiring error naming the env var (the composition root refuses to
// boot the poller without them).
func NewLiveReturnsGateway(cfg ReturnsConfig) (*LiveReturnsGateway, error) {
	c, err := newReturnsClient(cfg.BaseURL, cfg.VendorID, cfg.HTTPClient, cfg.Logger)
	if err != nil {
		return nil, err
	}
	return &LiveReturnsGateway{client: c, logger: cfg.Logger}, nil
}

// PollAdvices implements ports.NetworkReturnsGateway: every page of one
// poll, mapped onto the advice view's vocabulary. Re-delivery is normal and
// harmless — the feed's upsert is idempotent.
func (g *LiveReturnsGateway) PollAdvices(ctx context.Context, readyAfter time.Time) ([]views.ReturnAdvice, error) {
	dtos, err := g.client.pollAdvices(ctx, readyAfter)
	if err != nil {
		return nil, err
	}
	out := make([]views.ReturnAdvice, 0, len(dtos))
	seen := make(map[string]bool, len(dtos))
	for _, dto := range dtos {
		if seen[dto.AuthorizationID] {
			continue // the same advice twice inside one poll's pages
		}
		seen[dto.AuthorizationID] = true
		out = append(out, adviceFromDTO(dto))
	}
	return out, nil
}

// SubmitReturnOutcome implements ports.NetworkReturnsGateway: the one write
// of an advice's graded result, idempotent by the derived key.
func (g *LiveReturnsGateway) SubmitReturnOutcome(ctx context.Context, out views.ReturnOutcome) (string, error) {
	return g.client.submitOutcome(ctx, out)
}

// OutcomeTransaction implements ports.NetworkReturnsGateway: the network's
// verdict of an accepted submission (its statuses map onto our constants).
func (g *LiveReturnsGateway) OutcomeTransaction(ctx context.Context, transactionID string) (views.ReturnOutcomeTransaction, error) {
	tx, err := g.client.transaction(ctx, transactionID)
	if err != nil {
		return views.ReturnOutcomeTransaction{}, err
	}
	switch tx.Status {
	case "SUCCESS":
		return views.ReturnOutcomeTransaction{Status: views.TransactionSucceeded}, nil
	case "FAILURE":
		return views.ReturnOutcomeTransaction{Status: views.TransactionFailed, Errors: joinProblemErrors(tx.Errors)}, nil
	default: // PROCESSING and anything the network adds later
		return views.ReturnOutcomeTransaction{Status: views.TransactionProcessing}, nil
	}
}

// adviceFromDTO maps the network's wire shape onto the view's vocabulary;
// unknown enum values pass through as strings (the application records
// them and never branches on them — the disposition is an INPUT).
func adviceFromDTO(dto adviceDTO) views.ReturnAdvice {
	a := views.ReturnAdvice{
		AuthorizationID: dto.AuthorizationID,
		OrderID:         dto.OrderID,
		GroupNo:         dto.GroupNo,
		VendorID:        dto.VendorID,
		NodeID:          dto.NodeID,
		Disposition:     dto.Disposition,
		Method:          dto.Method,
		State:           dto.State,
		ExpiresAt:       dto.ExpiresAt.UTC(),
		TrackingNumbers: append([]string(nil), dto.TrackingNumbers...),
		ReadyAt:         dto.ReadyAt.UTC(),
	}
	for _, l := range dto.Lines {
		a.Lines = append(a.Lines, views.ReturnAdviceLine{
			LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, Quantity: l.Quantity, OwnerRef: l.OwnerRef,
		})
	}
	return a
}

func joinProblemErrors(errs []submissionErrorDTO) string {
	out := ""
	for i, e := range errs {
		if i > 0 {
			out += "; "
		}
		out += e.Code + ": " + e.Message
	}
	return out
}
