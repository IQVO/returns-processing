// Command api is returns-processing's composition root. It is built step by
// step on this branch; today it is the outbox relay host: it opens the real
// Postgres pool (migrations applied), builds the fleet's acks=all Kafka
// writer and drains the transactional outbox to
// warehouse.returns-processing.events (poison rows to its .dlq). The REST
// surface and the consumers arrive in later steps in this same root.
//
// The EventPublisher side needs no wiring here: the use cases receive the
// Postgres Outbox as a ports.EventPublisher when their inbound adapters
// land; this host only runs the background loop that publishes what those
// transactions committed.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	outboundkafka "github.com/claudioed/returns-processing/internal/adapters/outbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadRelayConfig()
	if err != nil {
		return err
	}
	logger := newLogger(getenv("LOG_LEVEL", "info"))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.databaseURL)
	if err != nil {
		logger.Error("postgres pool failed", "error", err)
		return err
	}
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

	logger.Info("outbox relay host running",
		"brokers", cfg.brokers, "interval", cfg.interval.String(),
		"max_attempts", cfg.maxAttempts, "events_topic", outboundkafka.TopicEvents, "dlq_topic", outboundkafka.TopicDLQ)
	if err := relay.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("outbox relay stopped", "error", err)
		return err
	}
	logger.Info("outbox relay host stopped")
	return nil
}
