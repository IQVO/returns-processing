// Command returns-reports is the read-only HTTP reader of the
// returns-processing analytics read side (ADR 0007):
// GET /reports/disposition-mix, GET /reports/value-recovery,
// GET /reports/return-cycle-time, GET /reports/freshness and /healthz on
// :8092, answered from the ANALYTICAL database only (a read-only pool; the
// database role should be read-only too). It writes nothing and never opens
// the OLTP database or Kafka. No auth layer: access control is the
// in-cluster boundary (fleet rule).
//
// Environment:
//
//	ANALYTICS_DATABASE_URL  REQUIRED. Analytical Postgres DSN, ideally a read-only role
//	HTTP_ADDR               listen address (default :8092)
//	LOG_LEVEL               debug | info | warn (or warning) | error (default info)
//	OTEL_SERVICE_NAME       service.name of spans and metrics (default returns-reports)
//	OTEL_EXPORTER_OTLP_ENDPOINTCollector OTLP/gRPC endpoint (default localhost:4317)
//	SERVICE_VERSION         service.version (default dev)
//	ENVIRONMENT             deployment.environment.name (default local)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	inboundhttp "github.com/claudioed/returns-processing/internal/adapters/inbound/http"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/telemetry"
)

const shutdownTimeout = 10 * time.Second

var errMissingAnalyticsURL = errors.New("ANALYTICS_DATABASE_URL is required: the reports are served from the analytical database")

func main() {
	if err := run(); err != nil {
		slog.Error("returns-reports exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Telemetry comes up before the router is built: otelchimetric binds the
	// global MeterProvider on construction.
	telCfg := loadTelemetryEnv(os.Getenv)
	flushTelemetry, err := startTelemetry(logger, telCfg)
	if err != nil {
		return err
	}
	defer flushTelemetry()

	url := os.Getenv("ANALYTICS_DATABASE_URL")
	if url == "" {
		return errMissingAnalyticsURL
	}

	ctx := context.Background()
	pool, err := analyticsstore.NewReadOnlyPool(ctx, url)
	if err != nil {
		logger.Error("read-only analytics pool failed", "error", err)
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		logger.Error("analytics database ping failed", "error", err)
		return err
	}

	httpAddr := getenv("HTTP_ADDR", ":8092")
	srv := &http.Server{
		Addr: httpAddr,
		Handler: inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{
			Reader: analyticsstore.NewReader(pool), Logger: logger, ServiceName: telCfg.serviceName,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("reports server listening", "addr", httpAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	sig, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serveErr:
		return fmt.Errorf("reports server failed: %w", err)
	case <-sig.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newLogger builds the JSON logger; the handler is wrapped so a record written
// with a span-carrying context also carries trace_id and span_id.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
