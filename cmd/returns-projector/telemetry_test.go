package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/telemetry"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLoadTelemetryEnvDefaultsToTheProjectorServiceName(t *testing.T) {
	got := loadTelemetryEnv(func(string) string { return "" })
	if got.serviceName != "returns-projector" || got.otlpEndpoint != telemetry.DefaultEndpoint || got.serviceVersion != telemetry.DefaultServiceVersion {
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

func TestStartTelemetryDoesNotBlockWithoutACollector(t *testing.T) {
	tp, mp, prop := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		otel.SetTextMapPropagator(prop)
	})
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

// A binary that serves no REST still correlates its logs: a record written
// with a span-carrying context carries that span's trace_id and span_id, and
// LOG_LEVEL is still honoured.
func TestNewLoggerStampsTheActiveSpanAndHonoursTheLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := newLoggerTo(&buf, "warn")
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))

	logger.InfoContext(ctx, "below the level")
	if buf.Len() != 0 {
		t.Fatalf("LOG_LEVEL=warn let an INFO record through: %s", buf.String())
	}
	logger.WarnContext(ctx, "consumer lagging")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log output is not one JSON line: %q (%v)", buf.String(), err)
	}
	if line[telemetry.TraceIDKey] != traceID.String() || line[telemetry.SpanIDKey] != spanID.String() {
		t.Fatalf("log ids = %v/%v, want %s/%s", line[telemetry.TraceIDKey], line[telemetry.SpanIDKey], traceID, spanID)
	}
}
