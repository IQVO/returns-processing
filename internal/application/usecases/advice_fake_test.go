package usecases_test

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// fakeReturnsGateway scripts the network seam for the ADR 0012 use-case
// tests: what PollAdvices hands back, what SubmitReturnOutcome answers and
// what the transaction reads say.
type fakeReturnsGateway struct {
	ports.NetworkReturnsGateway

	polled   []time.Time
	advices  []views.ReturnAdvice
	pollErr  error
	submits  []views.ReturnOutcome
	submitTx string
	// submitErr, when set, is answered for EVERY submission.
	submitErr error
	txs       map[string]views.ReturnOutcomeTransaction
	txErr     error
}

func (g *fakeReturnsGateway) PollAdvices(_ context.Context, readyAfter time.Time) ([]views.ReturnAdvice, error) {
	g.polled = append(g.polled, readyAfter)
	if g.pollErr != nil {
		return nil, g.pollErr
	}
	var out []views.ReturnAdvice
	for _, a := range g.advices {
		if readyAfter.IsZero() || a.ReadyAt.After(readyAfter) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (g *fakeReturnsGateway) SubmitReturnOutcome(_ context.Context, out views.ReturnOutcome) (string, error) {
	g.submits = append(g.submits, out)
	if g.submitErr != nil {
		return "", g.submitErr
	}
	return g.submitTx, nil
}

func (g *fakeReturnsGateway) OutcomeTransaction(_ context.Context, transactionID string) (views.ReturnOutcomeTransaction, error) {
	if g.txErr != nil {
		return views.ReturnOutcomeTransaction{}, g.txErr
	}
	tx, ok := g.txs[transactionID]
	if !ok {
		return views.ReturnOutcomeTransaction{Status: views.TransactionProcessing}, nil
	}
	return tx, nil
}

// errRefusedOutcome is a definitive refusal (wraps ports.ErrOutcomeRefused).
var errRefusedOutcome = errors.Join(ports.ErrOutcomeRefused, errors.New("network: outcome-refused: nope"))

// adviceStore is the combined advice-view port pair (the memory double and
// the failing wrapper both satisfy it; the poll and drain use cases take the
// halves separately).
type adviceStore interface {
	ports.ReturnAdviceFeed
	ports.ReturnAdviceViews
}

// failingAdviceStore fails the upsert of one advice id: a store outage the
// poll tick must stop at, before the watermark passes it.
type failingAdviceStore struct {
	adviceStore
	failID string
}

func (s *failingAdviceStore) ApplyReturnAdvice(ctx context.Context, a views.ReturnAdvice) error {
	if a.AuthorizationID == s.failID {
		return errors.New("advice store down")
	}
	return s.adviceStore.ApplyReturnAdvice(ctx, a)
}
