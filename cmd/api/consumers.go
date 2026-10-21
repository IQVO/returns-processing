package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	inboundkafka "github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// The three read-model consumers (ADR 0003) and their pinned group-id
// environment variables (.claude/rules/integration-events.md). The group ids
// were read — and required — at boot long before this step; here they are
// finally spent.

// backgroundConsumer is the part of an inbound consumer the wiring drives,
// kept small so the supervision itself is testable without a broker.
type backgroundConsumer interface {
	Run(ctx context.Context) error
	Close() error
}

// consumerBuilder builds one consumer once its group id is resolved from the
// environment variable env.
type consumerBuilder func(brokers []string, group string, logger *slog.Logger) backgroundConsumer

// consumerBuilders maps each pinned group-id variable to its consumer.
func consumerBuilders(ad adapters) map[string]consumerBuilder {
	return map[string]consumerBuilder{
		"PRODUCT_CLASSIFICATION_CONSUMER_GROUP": func(brokers []string, group string, logger *slog.Logger) backgroundConsumer {
			uc := &usecases.ApplyProductClassification{UnitOfWork: ad.uow, Processed: ad.processed, Classes: ad.classFeed}
			return inboundkafka.NewProductClassesConsumer(brokers, group, uc, logger)
		},
		"ORDER_LINES_CONSUMER_GROUP": func(brokers []string, group string, logger *slog.Logger) backgroundConsumer {
			uc := &usecases.ApplyAllocations{UnitOfWork: ad.uow, Processed: ad.processed, Lines: ad.lineFeed}
			return inboundkafka.NewAllocationsConsumer(brokers, group, uc, logger)
		},
		"MANIFEST_CONSUMER_GROUP": func(brokers []string, group string, logger *slog.Logger) backgroundConsumer {
			uc := &usecases.ApplyManifest{UnitOfWork: ad.uow, Processed: ad.processed, Units: ad.unitFeed}
			return inboundkafka.NewManifestsConsumer(brokers, group, uc, logger)
		},
	}
}

// consumerDeath is one consumer that stopped with an error that is not a
// clean shutdown.
type consumerDeath struct {
	name string
	env  string
	err  error
}

// consumerSet is the three consumers running in their own goroutines under
// one cancellable context, plus the channel their deaths are reported on.
type consumerSet struct {
	workers []*consumerWorker
	stop    context.CancelFunc
	// deaths receives one consumerDeath per consumer that stopped with an
	// error other than a clean context cancellation. It is buffered for
	// every worker, so a dying consumer never blocks its own goroutine.
	deaths chan consumerDeath
}

// consumerWorker supervises one running consumer.
type consumerWorker struct {
	name string
	env  string
	done chan struct{}
}

// startConsumers boots every pinned consumer under its environment-resolved
// group id. The readers dial lazily inside Run, so a broker outage (or a
// topic that does not exist yet) never blocks boot: the consumer logs and
// retries, and /readyz reports not-ready only while the broker is
// unreachable. Each worker closes its own reader and dead-letter writer once
// Run has returned.
func startConsumers(ad adapters, brokers []string, groups map[string]string, logger *slog.Logger) *consumerSet {
	builders := consumerBuilders(ad)
	ctx, stop := context.WithCancel(context.Background())
	set := &consumerSet{
		stop:   stop,
		deaths: make(chan consumerDeath, len(consumerGroupEnvs)),
	}
	for _, env := range consumerGroupEnvs {
		group := groups[env]
		c := builders[env](brokers, group, logger)
		w := &consumerWorker{name: consumerName(c), env: env, done: make(chan struct{})}
		set.workers = append(set.workers, w)
		go set.run(ctx, c, w, group, brokers, logger)
	}
	return set
}

// consumerName names the consumer in logs and deaths. The real consumers
// expose their Name; the test fakes do not.
func consumerName(c backgroundConsumer) string {
	if real, ok := c.(*inboundkafka.Consumer); ok {
		return real.Name
	}
	return "consumer"
}

// run drives one consumer until ctx is cancelled (a clean stop: nothing is
// reported) or it fails on its own (reported on deaths — the composition
// root then exits non-zero: a pod restart is the fleet's answer to a dead
// consumer, never serving on with a frozen local copy).
func (s *consumerSet) run(ctx context.Context, c backgroundConsumer, w *consumerWorker, group string, brokers []string, logger *slog.Logger) {
	defer close(w.done)
	defer func() {
		if err := c.Close(); err != nil {
			logger.Error("consumer close failed", "consumer", w.name, "error", err)
		}
	}()
	logger.Info("consumer running", "consumer", w.name, "group_id", group, "brokers", brokers)
	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		logger.Error("consumer stopped", "consumer", w.name, "error", err)
		s.deaths <- consumerDeath{name: w.name, env: w.env, err: err}
	}
}

// shutdown cancels every consumer and waits (bounded by workerDrainTimeout)
// for each to return.
func (s *consumerSet) shutdown(logger *slog.Logger) {
	s.stop()
	var wg sync.WaitGroup
	for _, w := range s.workers {
		wg.Add(1)
		go func(w *consumerWorker) {
			defer wg.Done()
			select {
			case <-w.done:
			case <-time.After(workerDrainTimeout):
				logger.Warn("consumer did not stop before the shutdown drain deadline", "consumer", w.name)
			}
		}(w)
	}
	wg.Wait()
}

// describe makes the death actionable in the process's final error.
func (d consumerDeath) describe() error {
	return fmt.Errorf("consumer %s (%s) stopped: %w", d.name, d.env, d.err)
}
