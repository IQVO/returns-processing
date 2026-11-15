package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/telemetry"
)

// defaultServiceName is the OTEL_SERVICE_NAME default: the name every span,
// metric and Loki `app` label of this binary carries.
const defaultServiceName = "returns-processing-a2a"

// telemetryFlushTimeout bounds the final export attempt. Without a deadline the
// exporter would retry against an unreachable Collector and stretch a shutdown
// out well past what an orchestrator will wait for.
const telemetryFlushTimeout = 5 * time.Second

// telemetryEnv is the observability part of the environment (ADR 0011).
//
//	OTEL_SERVICE_NAME            service.name of spans and metrics (default returns-processing-a2a)
//	OTEL_EXPORTER_OTLP_ENDPOINT  Collector OTLP/gRPC endpoint, host:port or URL (default telemetry.DefaultEndpoint)
//	SERVICE_VERSION              service.version (default telemetry.DefaultServiceVersion)
//	ENVIRONMENT                  deployment.environment.name, read by telemetry.Setup
type telemetryEnv struct {
	serviceName    string
	serviceVersion string
	otlpEndpoint   string
}

func loadTelemetryEnv(getenv func(string) string) telemetryEnv {
	return telemetryEnv{
		serviceName:    orDefault(getenv("OTEL_SERVICE_NAME"), defaultServiceName),
		serviceVersion: orDefault(getenv("SERVICE_VERSION"), telemetry.DefaultServiceVersion),
		otlpEndpoint:   orDefault(getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), telemetry.DefaultEndpoint),
	}
}

func orDefault(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// startTelemetry brings OpenTelemetry up and returns the flush func to defer.
// Export is non-blocking: an unreachable Collector costs telemetry, never
// availability. A failed flush is logged, never returned: turning that into a
// non-zero exit would make every clean shutdown look like a crash.
//
// It must run before the HTTP router is built: otelchimetric binds the global
// MeterProvider when the router is constructed.
func startTelemetry(logger *slog.Logger, cfg telemetryEnv) (func(), error) {
	shutdown, err := telemetry.Setup(context.Background(), cfg.serviceName, cfg.serviceVersion, cfg.otlpEndpoint)
	if err != nil {
		return nil, err
	}
	logger.Info("telemetry configured", "service_name", cfg.serviceName, "otlp_endpoint", cfg.otlpEndpoint)
	return func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
		defer cancel()
		if err := shutdown(flushCtx); err != nil {
			logger.Warn("telemetry flush failed on shutdown", "error", err)
		}
	}, nil
}
