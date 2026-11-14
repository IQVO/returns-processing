package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/claudioed/returns-processing/internal/application/ports"
)

// meterName scopes this service's own instruments, keeping them distinct from
// the ones otelchi and the runtime collector register.
const meterName = "github.com/claudioed/returns-processing"

// authorizationsDecidedName is the Tier 2 business metric of the context
// (ADR 0011, the fleet's standard metrics convention): every return
// authorization decision, by outcome.
const authorizationsDecidedName = "returns.authorizations.decided"

const (
	outcomeKey = attribute.Key("outcome")
	reasonKey  = attribute.Key("reason")

	outcomeAuthorized = "authorized"
	outcomeRejected   = "rejected"
)

// AuthorizationMetrics implements ports.AuthorizationMetrics against the
// global MeterProvider. Until Setup installs a real provider the global one is
// a no-op, so recording is cheap and safe in tests and local runs.
type AuthorizationMetrics struct {
	counter metric.Int64Counter
}

var _ ports.AuthorizationMetrics = (*AuthorizationMetrics)(nil)

// NewAuthorizationMetrics registers the counter. It only fails if the
// instrument name is invalid, which is a programming error, not a runtime
// condition. Call it after Setup so the instrument binds to the real
// MeterProvider.
func NewAuthorizationMetrics() (*AuthorizationMetrics, error) {
	counter, err := otel.Meter(meterName).Int64Counter(
		authorizationsDecidedName,
		metric.WithDescription("Return authorization decisions by outcome (authorized or rejected; reason says why a rejection happened, "+
			"as a problem slug such as return-not-authorizable or open-rma-exists). "+
			"A rising return-not-authorizable or open-rma-exists means clients ask for returns the rules refuse, "+
			"not that the service is failing; a rising internal-error or concurrent-modification means the service itself is."),
		metric.WithUnit("{authorization}"),
	)
	if err != nil {
		return nil, err
	}
	return &AuthorizationMetrics{counter: counter}, nil
}

// ReturnAuthorized counts a return that was authorized.
func (m *AuthorizationMetrics) ReturnAuthorized(ctx context.Context) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeAuthorized)))
}

// ReturnRejected counts an authorization that stored nothing, with the reason why.
func (m *AuthorizationMetrics) ReturnRejected(ctx context.Context, reason string) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeRejected), reasonKey.String(reason)))
}
