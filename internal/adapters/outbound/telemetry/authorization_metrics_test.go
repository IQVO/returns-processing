package telemetry_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/telemetry"
)

// installManualReader makes a ManualReader-backed MeterProvider the global one
// for the test: the instrument binds to it when it is created.
func installManualReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(previous) })
	return reader
}

func TestAuthorizationMetricsCountsByOutcomeAndReason(t *testing.T) {
	reader := installManualReader(t)

	metrics, err := telemetry.NewAuthorizationMetrics()
	if err != nil {
		t.Fatalf("NewAuthorizationMetrics: %v", err)
	}
	ctx := context.Background()
	metrics.ReturnAuthorized(ctx)
	metrics.ReturnAuthorized(ctx)
	metrics.ReturnRejected(ctx, "return-not-authorizable")
	metrics.ReturnRejected(ctx, "open-rma-exists")
	metrics.ReturnRejected(ctx, "open-rma-exists")

	got := collectAuthorizationPoints(t, reader)
	want := map[string]int64{
		"authorized/":                      2,
		"rejected/return-not-authorizable": 1,
		"rejected/open-rma-exists":         2,
	}
	if len(got) != len(want) {
		t.Fatalf("data points = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

// collectAuthorizationPoints reads the returns.authorizations.decided data
// points as "outcome/reason" -> value, checking the instrument's unit and
// that its description is present.
func collectAuthorizationPoints(t *testing.T, reader sdkmetric.Reader) map[string]int64 {
	t.Helper()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name == "returns.authorizations.decided" {
				addAuthorizationPoints(t, m, got)
			}
		}
	}
	return got
}

func addAuthorizationPoints(t *testing.T, m metricdata.Metrics, got map[string]int64) {
	t.Helper()
	if m.Unit != "{authorization}" || m.Description == "" {
		t.Errorf("unit = %q, description empty = %v; the convention wants a UCUM unit and a description", m.Unit, m.Description == "")
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("returns.authorizations.decided is a %T, want an int64 Sum", m.Data)
	}
	if !sum.IsMonotonic {
		t.Error("returns.authorizations.decided must be a monotonic counter")
	}
	for _, point := range sum.DataPoints {
		outcome, _ := point.Attributes.Value("outcome")
		reason, _ := point.Attributes.Value("reason")
		got[outcome.AsString()+"/"+reason.AsString()] = point.Value
	}
}

// Only outcome and reason are attribute keys of the counter: a third key would
// be a cardinality the convention did not review.
func TestAuthorizationMetricsAttributeKeysAreOutcomeAndReasonOnly(t *testing.T) {
	reader := installManualReader(t)
	metrics, err := telemetry.NewAuthorizationMetrics()
	if err != nil {
		t.Fatal(err)
	}
	metrics.ReturnAuthorized(context.Background())
	metrics.ReturnRejected(context.Background(), "invalid-quantity")

	for _, k := range authorizationAttributeKeys(t, reader) {
		if k != "outcome" && k != "reason" {
			t.Errorf("unexpected attribute key %q", k)
		}
	}
}

// authorizationAttributeKeys lists every attribute key of every data point of
// returns.authorizations.decided.
func authorizationAttributeKeys(t *testing.T, reader sdkmetric.Reader) []string {
	t.Helper()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok || m.Name != "returns.authorizations.decided" {
				continue
			}
			for _, point := range sum.DataPoints {
				for _, kv := range point.Attributes.ToSlice() {
					keys = append(keys, string(kv.Key))
				}
			}
		}
	}
	return keys
}

// Whatever the global provider is, recording is fire-and-forget.
func TestAuthorizationMetricsRecordingNeverPanics(t *testing.T) {
	metrics, err := telemetry.NewAuthorizationMetrics()
	if err != nil {
		t.Fatal(err)
	}
	metrics.ReturnAuthorized(context.Background())
	metrics.ReturnRejected(context.Background(), "internal-error")
}
