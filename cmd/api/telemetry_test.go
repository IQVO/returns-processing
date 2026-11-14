package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	inboundhttp "github.com/claudioed/returns-processing/internal/adapters/inbound/http"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/telemetry"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLoadTelemetryEnvDefaults(t *testing.T) {
	got := loadTelemetryEnv(envOf(nil))
	want := telemetryEnv{serviceName: "returns-processing", serviceVersion: telemetry.DefaultServiceVersion, otlpEndpoint: telemetry.DefaultEndpoint}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

func TestLoadTelemetryEnvReadsTheOTelContract(t *testing.T) {
	got := loadTelemetryEnv(envOf(map[string]string{
		"OTEL_SERVICE_NAME": "svc", "SERVICE_VERSION": "1.2.3", "OTEL_EXPORTER_OTLP_ENDPOINT": "collector:4317",
	}))
	want := telemetryEnv{serviceName: "svc", serviceVersion: "1.2.3", otlpEndpoint: "collector:4317"}
	if got != want {
		t.Fatalf("env = %+v, want %+v", got, want)
	}
}

// With nothing listening on the endpoint, bring-up and flush both return within
// their bounds: a missing Collector costs telemetry, never availability.
func TestStartTelemetryDoesNotBlockWithoutACollector(t *testing.T) {
	restoreGlobals(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		flush, err := startTelemetry(quiet(), telemetryEnv{serviceName: "t", serviceVersion: "t", otlpEndpoint: "127.0.0.1:14317"})
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

func restoreGlobals(t *testing.T) {
	t.Helper()
	tp, mp, prop := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		otel.SetTextMapPropagator(prop)
	})
}

func TestNewLoggerHonoursLogLevel(t *testing.T) {
	if !newLogger("warn").Handler().Enabled(context.Background(), slog.LevelWarn) || newLogger("warn").Handler().Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("newLogger ignores LOG_LEVEL")
	}
}

// buildServer hands the REST adapter the logger and service name its
// middleware needs, and the authorization counter to the use case.
func TestBuildServerWiresTheRouterIdentityAndTheAuthorizationCounter(t *testing.T) {
	logger := quiet()
	metrics := &countingAuthorizations{}
	s := buildServer(adapters{authMetrics: metrics}, nil, logger, "returns-processing")
	if s.Logger != logger || s.ServiceName != "returns-processing" {
		t.Errorf("Logger/ServiceName = %v/%q, want the ones passed in", s.Logger, s.ServiceName)
	}
	if s.AuthorizeReturn.Metrics != metrics {
		t.Error("AuthorizeReturn.Metrics is not the authorization counter handed to buildServer")
	}
}

type countingAuthorizations struct{}

func (*countingAuthorizations) ReturnAuthorized(context.Context)       {}
func (*countingAuthorizations) ReturnRejected(context.Context, string) {}

// The gate of the standard: one request yields an otelchi server span AND a log
// line whose trace_id/span_id are that span's, through the real logger wrapper.
func TestRouterLogCarriesTheServerSpanTraceID(t *testing.T) {
	restoreGlobals(t)
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	var buf bytes.Buffer
	logger := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(&buf, nil)))
	router := inboundhttp.NewRouter(&inboundhttp.Server{
		Logger:      logger,
		ServiceName: defaultServiceName,
	})

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/returns", nil))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
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
