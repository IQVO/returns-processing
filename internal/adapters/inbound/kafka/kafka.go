// Package kafka holds this service's INBOUND Kafka consumers (ADR 0003):
// the product classifications consumer (product-master's
// ProductClassified), the order lines consumer (order-management's
// OrderAllocated / OrderPartiallyAllocated) and the manifests consumer
// (fulfillment-execution's PackageManifested). Every consumer decodes
// CloudEvents 1.0 only, via internal/adapters/kafka/cloudevents, and
// dispatches on the FULL `type` string. The consumers only decode: every
// effect is a use case of internal/application/usecases, which claims the
// CloudEvents id and applies the copy in ONE unit of work.
//
// Delivery guarantee: AT-LEAST-ONCE with an ATOMIC effect. Each message is
// handled inside ONE ports.UnitOfWork (processed-event claim + effect commit
// or roll back together) and its offset is committed only AFTER handling
// returned nil (FetchMessage + CommitMessages). Handling returns a non-nil
// error ONLY for transient failures; the run loop then retries the SAME
// message with capped exponential backoff, and after a bounded number of
// attempts dead-letters it to "<topic>.dlq" so a stuck message cannot wedge
// the partition forever. Deterministic problems (not a CloudEvent, an
// unknown type) are logged at WARN and committed past; a payload of a
// handled type that can never be applied (usecases.ErrInvalidEvent) is
// dead-lettered at once.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// Reader is the subset of *kafkago.Reader a consumer needs, so unit tests
// never need a live broker. FetchMessage/CommitMessages (not ReadMessage)
// because ReadMessage with a GroupID auto-commits the offset the moment it
// returns, i.e. before the message has been handled: a failed handling would
// then never be redelivered. Offsets are committed explicitly, after success
// only.
type Reader interface {
	FetchMessage(ctx context.Context) (kafkago.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafkago.Message) error
	Close() error
}

// readerConfig builds the kafka-go reader configuration shared by the
// consumers. GroupID always comes from the caller (an environment variable at
// the composition root, never a literal here), satisfying
// internal/architecture's TestKafkaConsumerGroupNeverHardcodedInline.
//
// CommitInterval is deliberately left UNSET: with a GroupID kafka-go then
// commits synchronously inside CommitMessages, which the run loop calls only
// after a message was handled successfully. Setting it would batch the commit
// and reintroduce a window where an offset moves without the work being
// durable. A group without a committed offset starts at the first offset, so
// a fresh deployment replays the whole topic and the local copies converge.
//
// kafkago.NewReader does not dial the broker: the connection is opened by
// the first FetchMessage inside the consumer's own goroutine, so constructing
// a Reader never blocks process startup on Kafka being reachable.
func readerConfig(brokers []string, topic, groupID string) kafkago.ReaderConfig {
	return kafkago.ReaderConfig{
		Brokers: brokers,
		Topic:   topic,
		GroupID: groupID,
	}
}

func defaultLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

// readerErrorLogger adapts logger to kafka-go's ErrorLogger: kafka-go retries
// a failing group join, a missing topic or an unreachable broker silently
// unless it is given one, so every such failure is surfaced as an ERROR log
// line and the retry becomes visible.
func readerErrorLogger(logger *slog.Logger, consumer, topic string) kafkago.LoggerFunc {
	return func(format string, args ...any) {
		logger.Error("kafka reader error; retrying", "consumer", consumer, "topic", topic, "detail", fmt.Sprintf(format, args...))
	}
}

// RetryPolicy is the capped exponential backoff the run loop applies while
// the SAME message keeps failing with a transient error. Zero fields fall
// back to DefaultRetryInitial / DefaultRetryMax.
type RetryPolicy struct {
	Initial time.Duration
	Max     time.Duration
}

// Default backoff: 200ms, 400ms, 800ms ... capped at 5s.
const (
	DefaultRetryInitial = 200 * time.Millisecond
	DefaultRetryMax     = 5 * time.Second
)

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.Initial <= 0 {
		p.Initial = DefaultRetryInitial
	}
	if p.Max <= 0 {
		p.Max = DefaultRetryMax
	}
	return p
}

// next doubles d, capped at p.Max.
func (p RetryPolicy) next(d time.Duration) time.Duration {
	d *= 2
	if d > p.Max {
		return p.Max
	}
	return d
}

// sleepFunc waits d or until ctx is done (returning ctx.Err()). A field so
// tests can record the backoff sequence without really sleeping.
type sleepFunc func(ctx context.Context, d time.Duration) error

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// consumeLoop is the at-least-once run loop shared by the consumers.
type consumeLoop struct {
	reader Reader
	// handle applies one message; non-nil means transient failure => retry.
	handle func(ctx context.Context, msg kafkago.Message) error
	logger *slog.Logger
	name   string
	retry  RetryPolicy
	sleep  sleepFunc

	// maxAttempts and deadLetter bound the handling retry: once handle has
	// failed maxAttempts times for the SAME message, deadLetter is called
	// instead of retrying handle again (see deadletter.go). The zero value
	// (maxAttempts <= 0 or deadLetter nil) retries handle forever.
	maxAttempts int
	deadLetter  func(ctx context.Context, msg kafkago.Message, cause error) error
}

// run fetches one message at a time and does not fetch the next until the
// current one was handled successfully AND its offset committed. It returns
// only when ctx is cancelled or FetchMessage itself fails — the composition
// root exits non-zero on that (a dead consumer means a frozen local copy).
func (l *consumeLoop) run(ctx context.Context) error {
	policy := l.retry.withDefaults()
	sleep := l.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	for {
		msg, err := l.reader.FetchMessage(ctx)
		if err != nil {
			return err
		}
		if err := l.handleWithDeadLetter(ctx, policy, sleep, msg); err != nil {
			return err
		}
		// Commit ONLY now. A commit that fails is retried too: the work is
		// already durable, and if the process dies first the redelivery is
		// skipped by the processed-event claim.
		if err := l.retryUntilOK(ctx, policy, sleep, "offset commit", msg, func() error {
			return l.reader.CommitMessages(ctx, msg)
		}); err != nil {
			return err
		}
	}
}

// handleWithDeadLetter retries l.handle on msg with capped backoff, exactly
// like retryUntilOK, UNLESS l.maxAttempts/l.deadLetter are set: then, once
// l.maxAttempts handling attempts have all failed (or at once for a poison
// message), it publishes msg to the dead letter sink instead of retrying
// handle again. A handler that failed because ctx was cancelled (shutdown) is
// never dead-lettered: the offset stays uncommitted and the message is
// redelivered on restart. The dead-letter PUBLISH itself is retried forever
// (via retryUntilOK): a message that could not even be dead-lettered must
// never be silently dropped by committing past it.
func (l *consumeLoop) handleWithDeadLetter(ctx context.Context, p RetryPolicy, sleep sleepFunc, msg kafkago.Message) error {
	if l.maxAttempts <= 0 || l.deadLetter == nil {
		return l.retryUntilOK(ctx, p, sleep, "handling", msg, func() error { return l.handle(ctx, msg) })
	}

	delay := p.Initial
	for attempt := 1; attempt <= l.maxAttempts; attempt++ {
		err := l.handle(ctx, msg)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errPoison) {
			l.logger.ErrorContext(ctx, l.name+" message can never be applied; dead-lettering it without retry",
				"error", err, "partition", msg.Partition, "offset", msg.Offset)
			return l.retryUntilOK(ctx, p, sleep, "dead-letter publish", msg, func() error {
				return l.deadLetter(ctx, msg, err)
			})
		}
		if attempt == l.maxAttempts {
			l.logger.ErrorContext(ctx, l.name+" handling failed after the maximum attempts; dead-lettering the message",
				"error", err, "partition", msg.Partition, "offset", msg.Offset, "attempts", attempt)
			return l.retryUntilOK(ctx, p, sleep, "dead-letter publish", msg, func() error {
				return l.deadLetter(ctx, msg, err)
			})
		}
		l.logger.ErrorContext(ctx, l.name+" handling failed; retrying the same message",
			"error", err, "partition", msg.Partition, "offset", msg.Offset,
			"attempt", attempt, "max_attempts", l.maxAttempts, "retry_in", delay)
		if serr := sleep(ctx, delay); serr != nil {
			return serr
		}
		delay = p.next(delay)
	}
	return nil // unreachable: the loop above always returns
}

// retryUntilOK runs op until it returns nil, sleeping with capped exponential
// backoff between attempts. It gives up (returning ctx.Err()) only when ctx is
// cancelled. It NEVER skips: a message that cannot be handled blocks its
// partition (and is logged at ERROR every attempt) rather than being dropped,
// because a drop is silent data loss.
func (l *consumeLoop) retryUntilOK(ctx context.Context, p RetryPolicy, sleep sleepFunc, what string, msg kafkago.Message, op func() error) error {
	delay := p.Initial
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.logger.ErrorContext(ctx, l.name+" "+what+" failed; retrying the same message",
			"error", err, "partition", msg.Partition, "offset", msg.Offset,
			"attempt", attempt, "retry_in", delay)
		if err := sleep(ctx, delay); err != nil {
			return err
		}
		delay = p.next(delay)
	}
}
