package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// ReturnAdvices is the double of the ADR 0012 advice view: the map the
// poller feeds and the completion hook and the drain read. It mirrors the
// Postgres adapter's rules exactly (the repocontract proves it): the upsert
// never touches the outcome columns, an outcome is stored exactly once, and
// the drain lists PENDING then SUBMITTED rows oldest first.
type ReturnAdvices struct {
	mu      sync.Mutex
	rows    map[string]views.ReturnAdvice
	outcome map[string]storedOutcome
}

type storedOutcome struct {
	body          views.ReturnOutcome
	transactionID string
	state         string // PENDING | SUBMITTED | FAILED
	refusal       string
	since         time.Time
}

// NewReturnAdvices returns an empty double.
func NewReturnAdvices() *ReturnAdvices {
	return &ReturnAdvices{rows: map[string]views.ReturnAdvice{}, outcome: map[string]storedOutcome{}}
}

// ApplyReturnAdvice implements ports.ReturnAdviceFeed: first sight or
// re-delivery upsert the network-owned facts only.
func (m *ReturnAdvices) ApplyReturnAdvice(_ context.Context, a views.ReturnAdvice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row, ok := m.rows[a.AuthorizationID]; ok {
		row.OrderID = a.OrderID
		row.GroupNo = a.GroupNo
		row.VendorID = a.VendorID
		row.NodeID = a.NodeID
		row.Disposition = a.Disposition
		row.Method = a.Method
		row.State = a.State
		row.Lines = append([]views.ReturnAdviceLine(nil), a.Lines...)
		row.TrackingNumbers = append([]string(nil), a.TrackingNumbers...)
		row.ReadyAt = a.ReadyAt
		row.ExpiresAt = a.ExpiresAt
		m.rows[a.AuthorizationID] = row
		return nil
	}
	if a.FirstSeenAt.IsZero() {
		a.FirstSeenAt = time.Now().UTC()
	}
	a.Lines = append([]views.ReturnAdviceLine(nil), a.Lines...)
	a.TrackingNumbers = append([]string(nil), a.TrackingNumbers...)
	a.OutcomeState = ""
	m.rows[a.AuthorizationID] = a
	return nil
}

// MarkOutcomePending implements ports.ReturnAdviceFeed.
func (m *ReturnAdvices) MarkOutcomePending(_ context.Context, authorizationID string, out views.ReturnOutcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[authorizationID]
	if !ok {
		return ports.ErrAdviceUnknown
	}
	if _, ok := m.outcome[authorizationID]; ok {
		return ports.ErrOutcomeAlreadyStored
	}
	m.outcome[authorizationID] = storedOutcome{body: out, state: views.AdviceOutcomePending, since: time.Now().UTC()}
	row.OutcomeState = views.AdviceOutcomePending
	m.rows[authorizationID] = row
	return nil
}

// MarkOutcomeSubmitted implements ports.ReturnAdviceFeed.
func (m *ReturnAdvices) MarkOutcomeSubmitted(_ context.Context, authorizationID, transactionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[authorizationID]
	if !ok {
		return ports.ErrAdviceUnknown
	}
	stored, ok := m.outcome[authorizationID]
	if !ok {
		return ports.ErrAdviceUnknown
	}
	stored.transactionID = transactionID
	stored.state = views.AdviceOutcomeSubmitted
	m.outcome[authorizationID] = stored
	row.OutcomeState = views.AdviceOutcomeSubmitted
	m.rows[authorizationID] = row
	return nil
}

// MarkOutcomeSucceeded implements ports.ReturnAdviceFeed (terminal,
// idempotent: a replayed success is a no-op).
func (m *ReturnAdvices) MarkOutcomeSucceeded(_ context.Context, authorizationID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[authorizationID]
	if !ok {
		return ports.ErrAdviceUnknown
	}
	stored, ok := m.outcome[authorizationID]
	if !ok || stored.state == views.AdviceOutcomeSucceeded {
		return nil
	}
	stored.state = views.AdviceOutcomeSucceeded
	m.outcome[authorizationID] = stored
	row.OutcomeState = views.AdviceOutcomeSucceeded
	m.rows[authorizationID] = row
	return nil
}

// MarkOutcomeFailed implements ports.ReturnAdviceFeed (terminal).
func (m *ReturnAdvices) MarkOutcomeFailed(_ context.Context, authorizationID, refusal string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[authorizationID]
	if !ok {
		return ports.ErrAdviceUnknown
	}
	stored, ok := m.outcome[authorizationID]
	if !ok {
		return ports.ErrAdviceUnknown
	}
	stored.state = views.AdviceOutcomeFailed
	stored.refusal = refusal
	m.outcome[authorizationID] = stored
	row.OutcomeState = views.AdviceOutcomeFailed
	m.rows[authorizationID] = row
	return nil
}

// Get implements ports.ReturnAdviceViews.
func (m *ReturnAdvices) Get(_ context.Context, authorizationID string) (views.ReturnAdvice, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[authorizationID]
	return row, ok, nil
}

// PendingOutcomes implements ports.ReturnAdviceViews: PENDING and SUBMITTED
// outcomes, oldest first; a FAILED one is terminal and never listed.
func (m *ReturnAdvices) PendingOutcomes(_ context.Context) ([]views.PendingReturnOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []views.PendingReturnOutcome
	for _, stored := range m.outcome {
		if stored.state != views.AdviceOutcomePending && stored.state != views.AdviceOutcomeSubmitted {
			continue
		}
		out = append(out, views.PendingReturnOutcome{Outcome: stored.body, TransactionID: stored.transactionID, PendingSince: stored.since})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PendingSince.Before(out[j].PendingSince) })
	return out, nil
}

// LatestReadyAt implements ports.ReturnAdviceViews (the poll watermark).
func (m *ReturnAdvices) LatestReadyAt(_ context.Context) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest time.Time
	for _, row := range m.rows {
		if row.ReadyAt.After(latest) {
			latest = row.ReadyAt
		}
	}
	return latest, !latest.IsZero(), nil
}

// Refusal returns the recorded refusal text of a FAILED outcome (test aid).
func (m *ReturnAdvices) Refusal(authorizationID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outcome[authorizationID].refusal
}
