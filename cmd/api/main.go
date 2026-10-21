// Command api is returns-processing's composition root: it wires env config
// into adapters, adapters into use cases, and use cases into the HTTP router,
// the outbox relay and the three Kafka read-model consumers. Nothing else in
// the module imports it.
//
// Postgres and Kafka are the ONLY runtime: there is no in-memory mode and no
// optional relay. A missing mandatory variable is a boot error naming it,
// raised before the listener binds.
//
// The service never creates topics through an admin API: the cluster broker
// auto-creates them on first write (the relay's writer and the consumers'
// dead-letter writers set AllowAutoTopicCreation) and infrastructure declares
// none.
//
// Environment:
//
//	DATABASE_URL                        REQUIRED. Postgres DSN
//	KAFKA_BROKERS                       REQUIRED. Comma-separated brokers
//	PRODUCT_CLASSIFICATION_CONSUMER_GROUP
//	ORDER_LINES_CONSUMER_GROUP           REQUIRED. The three pinned consumer
//	MANIFEST_CONSUMER_GROUP             group ids (integration-events.md):
//	                                    product-master's ProductClassified,
//	                                    order-management's OrderAllocated /
//	                                    OrderPartiallyAllocated and
//	                                    fulfillment-execution's
//	                                    PackageManifested feed the local
//	                                    copies (ADR 0003)
//	OUTBOX_RELAY_INTERVAL               relay poll interval (Go duration or seconds, default 1s)
//	OUTBOX_MAX_ATTEMPTS                 failures before the relay excludes a row (default 20, 0 = unlimited)
//	HTTP_ADDR                           listen address (default :8080)
//	SHUTDOWN_DRAIN_DELAY                wait after flipping /readyz before closing (default 5s, 0 = none)
//	LOG_LEVEL                           debug | info | warn | error (default info)
//
// A consumer that dies is a process death: serveUntilSignal shuts the service
// down and run returns the consumer's error, so main exits non-zero and the
// pod restarts — a half-alive service serving on frozen local copies is worse
// than a blip. The readers dial lazily inside Run, so an unreachable broker at
// boot is retry logs, never a death.
//
// /readyz is the drain gate AND a Postgres ping AND a Kafka metadata check
// (a broker answers a metadata request that names no topic: reachability
// only, never topics), cached for a few seconds. /healthz is liveness only.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	inboundhttp "github.com/claudioed/returns-processing/internal/adapters/inbound/http"
	outboundkafka "github.com/claudioed/returns-processing/internal/adapters/outbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// workerDrainTimeout bounds how long shutdown waits for a background worker
// (a consumer, the relay) to return after its context is cancelled.
const workerDrainTimeout = 10 * time.Second

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

// run boots the service and serves until SIGINT/SIGTERM or until ctx is
// cancelled (the in-process tests stop it that way), then shuts down in the
// fleet order. It reads the whole configuration from the environment.
func run(ctx context.Context) error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	cfg, err := loadBootConfig()
	if err != nil {
		return err
	}

	pool, err := postgres.NewPool(ctx, cfg.databaseURL)
	if err != nil {
		logger.Error("postgres pool failed", "error", err)
		return err
	}
	// pool.Close is deferred FIRST so it runs LAST, after the HTTP drain,
	// the consumers and the relay have stopped.
	defer pool.Close()
	if err := postgres.RunMigrations(cfg.databaseURL); err != nil {
		logger.Error("migrations failed", "error", err)
		return err
	}

	sink := outboundkafka.NewRelaySink(cfg.brokers)
	defer func() {
		if err := sink.Close(); err != nil {
			logger.Error("kafka relay sink close failed", "error", err)
		}
	}()

	// The attempt cap is a deliberate setting of this composition root, never
	// the unlimited default by omission (TestOutboxRelaysPassAttemptCap):
	// OUTBOX_MAX_ATTEMPTS, default 20, 0 = unlimited.
	relay := postgres.NewOutboxRelay(pool, outboundkafka.NewEncoder(), sink,
		postgres.WithLogger(logger),
		postgres.WithInterval(cfg.interval),
		postgres.WithMaxAttempts(cfg.maxAttempts),
	)

	ad := buildAdapters(pool)
	consumers := startConsumers(ad, cfg.brokers, cfg.groups, logger)
	readiness := buildReadiness(ad, outboundkafka.NewBrokerCheck(cfg.brokers).Check)
	httpServer := &http.Server{
		Addr:              cfg.httpAddr,
		Handler:           inboundhttp.NewRouter(buildServer(ad, readiness)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("returns-processing running",
		"brokers", cfg.brokers, "http_addr", cfg.httpAddr,
		"relay_interval", cfg.interval.String(), "max_attempts", cfg.maxAttempts,
		"events_topic", outboundkafka.TopicEvents, "dlq_topic", outboundkafka.TopicDLQ,
		"consumers", len(consumers.workers))
	return serveUntilSignal(ctx, logger, httpServer, readiness, relay, consumers)
}

// serveUntilSignal runs httpServer, the consumers and the relay until
// SIGINT/SIGTERM, until ctx is cancelled, until a listen error, or until a
// worker dies, then shuts down in the fleet order: (1) /readyz flips to 503,
// (2) wait the drain delay, (3) drain HTTP, (4) stop and await the consumers,
// (5) stop and await the relay LAST so nothing committed above is stranded;
// the caller's deferred pool.Close runs after all of it. A consumer death
// takes the same exit but returns the consumer's error, so the process exits
// non-zero and the pod restarts (the fleet rule: never serve on with a dead
// consumer's frozen copy).
func serveUntilSignal(ctx context.Context, logger *slog.Logger, httpServer *http.Server,
	readiness *inboundhttp.Readiness, relay *postgres.OutboxRelay, consumers *consumerSet,
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	relayDone := make(chan error, 1)
	relayCtx, stopRelay := context.WithCancel(ctx)
	defer stopRelay() // idempotent; every exit path releases the relay
	go func() {
		if err := relay.Run(relayCtx); err != nil && !errors.Is(err, context.Canceled) {
			relayDone <- err
			return
		}
		relayDone <- nil
	}()

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		consumers.shutdown(logger)
		awaitRelay(logger, relayDone)
		return err
	case err := <-relayDone:
		// The relay stopped on its own: nothing can leave the service any
		// more, so the process refuses to keep serving.
		_ = httpServer.Close()
		consumers.shutdown(logger)
		if err != nil {
			return err
		}
		return errors.New("outbox relay stopped")
	case d := <-consumers.deaths:
		// A consumer died: shut down in the same fleet order and exit
		// non-zero with the consumer's error (a pod restart is the answer).
		logger.Error("consumer stopped; shutting down for a pod restart", "consumer", d.name, "env", d.env, "error", d.err)
		drain(logger, readiness)
		err := d.describe()
		err = errors.Join(err, shutdownHTTP(httpServer))
		consumers.shutdown(logger)
		stopRelay()
		awaitRelay(logger, relayDone)
		return err
	case <-sigCtx.Done():
	}

	drain(logger, readiness)
	err := shutdownHTTP(httpServer)
	consumers.shutdown(logger)

	stopRelay() // belt to the deferred braces: stop now, not at return
	awaitRelay(logger, relayDone)
	return err
}

// drain flips /readyz to not-ready and waits out the drain delay so the load
// balancer pulls the instance before anything closes.
func drain(logger *slog.Logger, readiness *inboundhttp.Readiness) {
	readiness.SetNotReady()
	if delay := shutdownDrainDelay(); delay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", delay.String())
		time.Sleep(delay)
	}
}

// shutdownHTTP gracefully drains the HTTP server within shutdownTimeout.
func shutdownHTTP(httpServer *http.Server) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// awaitRelay waits (bounded) for the relay goroutine to return and logs an
// unexpected relay error (a clean stop reports nil).
func awaitRelay(logger *slog.Logger, done <-chan error) {
	select {
	case err := <-done:
		if err != nil {
			logger.Error("outbox relay stopped with an error", "error", err)
		}
	case <-time.After(workerDrainTimeout):
		logger.Warn("outbox relay did not stop before the shutdown drain deadline")
	}
}
