package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	inbounda2a "github.com/claudioed/returns-processing/internal/adapters/inbound/a2a"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/telemetry"
)

func TestLoadTelemetryEnvDefaultsToTheA2AServiceName(t *testing.T) {
	got := loadTelemetryEnv(func(string) string { return "" })
	if got.serviceName != "returns-processing-a2a" || got.otlpEndpoint != telemetry.DefaultEndpoint || got.serviceVersion != telemetry.DefaultServiceVersion {
		t.Fatalf("defaults = %+v", got)
	}
}

func TestLoadTelemetryEnvReadsTheOTelContract(t *testing.T) {
	env := map[string]string{"OTEL_SERVICE_NAME": "svc", "SERVICE_VERSION": "1.2.3", "OTEL_EXPORTER_OTLP_ENDPOINT": "collector:4317"}
	got := loadTelemetryEnv(func(k string) string { return env[k] })
	want := telemetryEnv{serviceName: "svc", serviceVersion: "1.2.3", otlpEndpoint: "collector:4317"}
	if got != want {
		t.Fatalf("env = %+v, want %+v", got, want)
	}
}

func restoreGlobals(t *testing.T) {
	t.Helper()
	tp, mp, prop := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		otel.SetTextMapPropagator(prop)
	})
}

func TestStartTelemetryDoesNotBlockWithoutACollector(t *testing.T) {
	restoreGlobals(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		flush, err := startTelemetry(quietLogger(), telemetryEnv{serviceName: "t", serviceVersion: "t", otlpEndpoint: "127.0.0.1:14317"})
		if err != nil {
			t.Errorf("startTelemetry: %v", err)
			return
		}
		flush()
	}()
	select {
	case <-done:
	case <-time.After(3*telemetryFlushTimeout + 5*time.Second):
		t.Fatal("startTelemetry or its flush blocked on an unreachable collector")
	}
}

func newObservedAgent(logger *slog.Logger) http.Handler {
	return newAgent(inbounda2a.Deps{}, testAgentURL, "test", logger, "returns-processing-a2a")
}

// One A2A request yields a server span and a request log line whose
// trace_id/span_id are that span's.
func TestRouterLogCarriesTheServerSpanTraceID(t *testing.T) {
	restoreGlobals(t)
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	var buf bytes.Buffer
	logger := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(&buf, nil)))

	newObservedAgent(logger).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].SpanKind() != trace.SpanKindServer {
		t.Fatalf("spans = %d, want one server span", len(spans))
	}
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log output is not one JSON line: %q (%v)", buf.String(), err)
	}
	sc := spans[0].SpanContext()
	if line[telemetry.TraceIDKey] != sc.TraceID().String() || line[telemetry.SpanIDKey] != sc.SpanID().String() {
		t.Fatalf("log ids = %v/%v, want the server span's %s/%s", line[telemetry.TraceIDKey], line[telemetry.SpanIDKey], sc.TraceID(), sc.SpanID())
	}
	if id, _ := line["request_id"].(string); id == "" {
		t.Error("request_id missing from the request line")
	}
}

func TestRouterDoesNotLogTheLivenessProbe(t *testing.T) {
	var buf bytes.Buffer
	newObservedAgent(slog.New(slog.NewJSONHandler(&buf, nil))).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if buf.Len() != 0 {
		t.Fatalf("/healthz was logged: %s", buf.String())
	}
}

// Fleet observability standard, Tier 1: the A2A router emits
// http.server.request.duration like every other router of the fleet.
func TestNewAgent_EmitsStandardHTTPServerMetrics(t *testing.T) {
	restoreGlobals(t)
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	router := newObservedAgent(nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "http.server.request.duration" {
				return
			}
		}
	}
	t.Error("http.server.request.duration not emitted by the A2A router")
}
