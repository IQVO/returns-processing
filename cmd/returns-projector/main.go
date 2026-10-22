// Command returns-projector is the WRITER of the returns-processing
// analytics read side (ADR 0007). It consumes the analytics topic
// (warehouse.returns-processing.analytics) under a fixed, env-supplied
// consumer group, projects each event into the ANALYTICAL database (a
// separate database with its own embedded migrations) and serves only
// /healthz and /readyz on an admin port. It is the only writer of that
// database; the read-only reports binary is cmd/returns-reports.
//
// It never touches the OLTP database and calls no sibling context. Kafka is
// dialled lazily (the reader connects inside its first fetch), so a broker
// outage at boot does not crash the process. Like cmd/api it has no
// in-memory mode: a missing variable is a boot error naming it.
//
// Environment:
//
//	ANALYTICS_DATABASE_URL    REQUIRED. Analytical Postgres DSN, DIRECT
//	                          (golang-migrate's advisory lock does not
//	                          survive PgBouncer transaction pooling)
//	KAFKA_BROKERS             REQUIRED. Comma-separated brokers
//	ANALYTICS_CONSUMER_GROUP  REQUIRED. The projector's fixed consumer group
//	ADMIN_ADDR                /healthz and /readyz listen address (default :8091)
//	LOG_LEVEL                 debug | info | warn (or warning) | error (default info)
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundkafka "github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/analyticsstore"
	outboundkafka "github.com/claudioed/returns-processing/internal/adapters/outbound/kafka"
)

// shutdownTimeout bounds the admin server drain and the wait for the consumer
// loop to commit/dead-letter the message it was handling.
const shutdownTimeout = 10 * time.Second

// config is the projector's environment.
type config struct {
	analyticsURL string
	brokers      []string
	group        string
	adminAddr    string
	logLevel     string
}

var (
	errMissingAnalyticsURL = errors.New("ANALYTICS_DATABASE_URL is required: the projector writes only to the analytical database")
	errMissingBrokers      = errors.New("KAFKA_BROKERS is required: the projector consumes the analytics topic")
	errMissingGroup        = errors.New("ANALYTICS_CONSUMER_GROUP is required: the projector's consumer group comes from the environment")
)

// loadConfig reads the environment through getenv (os.Getenv in main). Every
// problem is reported together.
func loadConfig(getenv func(string) string) (config, error) {
	or := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	c := config{
		analyticsURL: getenv("ANALYTICS_DATABASE_URL"),
		group:        getenv("ANALYTICS_CONSUMER_GROUP"),
		adminAddr:    or("ADMIN_ADDR", ":8091"),
		logLevel:     or("LOG_LEVEL", "info"),
	}
	for _, b := range strings.Split(getenv("KAFKA_BROKERS"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			c.brokers = append(c.brokers, b)
		}
	}
	var problems []error
	if c.analyticsURL == "" {
		problems = append(problems, errMissingAnalyticsURL)
	}
	if len(c.brokers) == 0 {
		problems = append(problems, errMissingBrokers)
	}
	if c.group == "" {
		problems = append(problems, errMissingGroup)
	}
	if len(problems) > 0 {
		return config{}, errors.Join(problems...)
	}
	return c, nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("returns-projector exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	logger := newLogger(cfg.logLevel)
	slog.SetDefault(logger)

	pool, err := openAnalyticsPool(context.Background(), logger, cfg.analyticsURL)
	if err != nil {
		return err
	}
	defer pool.Close() // runs last: after the consumer loop has stopped touching it

	consumer := inboundkafka.NewAnalyticsConsumer(cfg.brokers, outboundkafka.TopicAnalytics, cfg.group,
		analyticsstore.NewProjection(pool), logger)
	defer func() {
		if err := consumer.Close(); err != nil {
			logger.Error("closing the analytics consumer failed", "error", err)
		}
	}()

	var notReady atomic.Bool
	admin := &http.Server{Addr: cfg.adminAddr, Handler: newAdminMux(&notReady), ReadHeaderTimeout: 5 * time.Second}
	adminErr := make(chan error, 1)
	go func() {
		logger.Info("projector admin server listening", "addr", cfg.adminAddr)
		if err := admin.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			adminErr <- err
		}
	}()

	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	defer cancelConsumer()
	consumerDone := make(chan error, 1)
	go func() {
		logger.Info("analytics consumer running", "topic", outboundkafka.TopicAnalytics, "group_id", cfg.group, "brokers", cfg.brokers)
		consumerDone <- consumer.Run(consumerCtx)
	}()

	sig, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := awaitStop(sig.Done(), consumerDone, adminErr); err != nil {
		return err
	}

	// Graceful shutdown: flip readiness first, drain the admin server, then
	// let the consumer finish the message in flight (commit or dead-letter)
	// before the pool closes.
	notReady.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := admin.Shutdown(ctx)
	cancelConsumer()
	select {
	case err := <-consumerDone:
		// Cancellation is the expected end; anything else deserves a line in
		// the log even though the process is exiting anyway.
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("analytics consumer returned an error at shutdown", "error", err)
		}
	case <-ctx.Done():
		logger.Warn("analytics consumer did not stop before the shutdown deadline")
	}
	return shutdownErr
}

// awaitStop blocks until the process must stop. A consumer that returned or
// an admin server that failed is an ERROR, never a quiet exit: a projector
// whose consumer loop died must not keep answering its probes and consume
// nothing, so run returns it and main exits non-zero. A signal is the only
// clean stop (nil).
func awaitStop(signalled <-chan struct{}, consumerDone <-chan error, adminErr <-chan error) error {
	select {
	case err := <-consumerDone:
		// The consumer only returns on cancellation or an unrecoverable
		// reader failure: exit non-zero so the pod restarts.
		return fmt.Errorf("analytics consumer stopped unexpectedly: %w", err)
	case err := <-adminErr:
		return fmt.Errorf("admin server failed: %w", err)
	case <-signalled:
		return nil
	}
}

// newAdminMux serves /healthz (liveness, never flipped) and /readyz (flips to
// 503 as the first step of shutdown).
func newAdminMux(notReady *atomic.Bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if notReady.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	return mux
}

// openAnalyticsPool applies the embedded analytical migrations over the
// direct DSN, opens the writer pool and pings it.
func openAnalyticsPool(ctx context.Context, logger *slog.Logger, url string) (*pgxpool.Pool, error) {
	if err := analyticsstore.RunMigrations(url); err != nil {
		logger.Error("analytics migrations failed", "error", err)
		return nil, err
	}
	pool, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		logger.Error("analytics pool failed", "error", err)
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		logger.Error("analytics database ping failed", "error", err)
		return nil, err
	}
	return pool, nil
}

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
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
