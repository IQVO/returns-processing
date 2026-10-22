package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/returns-processing/internal/application/outbox"
)

// RelayEncoder maps a claimed outbox row to its Kafka wire form: one message
// per destination topic (the integration topic and its analytics twin,
// ADR 0007). The kafka adapter's Encoder is the production implementation;
// the poison contract is Encode's: ok=false + a cause means the row can never
// be sent, so it is dead-lettered instead of retried.
type RelayEncoder interface {
	Encode(row outbox.Row) (msgs []outbox.Message, cause string, ok bool)
	DeadLetter(row outbox.Row, cause string) outbox.Message
}

// RelaySink writes already-encoded messages to Kafka. kafka.RelaySink is the
// production implementation (one topic-less acks=all writer — every message,
// dead-letters included, carries its own topic); tests substitute a fake.
type RelaySink interface {
	Send(ctx context.Context, msgs ...outbox.Message) error
}

// OutboxRelay polls the outbox table for due rows (written in the same
// transaction as their aggregate by Outbox, the ports.EventPublisher side)
// and relays them to Kafka. It is the only path by which an outbox row ever
// reaches the broker — the publisher only ever inserts.
//
// One pass claims up to batchSize due rows under FOR UPDATE SKIP LOCKED (two
// overlapping relays never claim the same row) and walks them in id order,
// one row at a time, so a later event never overtakes an earlier one of the
// same aggregate that is still being retried:
//
//   - an encodable row is sent and, on success, DELETED in the same
//     transaction (delivery is at-least-once: a crash between send and commit
//     republishes the row with the SAME persisted event id, which is what lets
//     consumers dedupe);
//   - a poison row — one the encoder refuses (unknown event, malformed
//     payload) — is dead-lettered to warehouse.returns-processing.events.dlq
//     and then deleted: it is deterministic, no retry can fix it, and keeping
//     it would block every later row behind it;
//   - a send failure (broker down) records the error and stops the pass: the
//     row stays with attempts+1 and a pushed-back next_attempt_at, later rows
//     stay unpublished, and the next pass retries.
//
// With WithMaxAttempts(n), n > 0, a row that has failed n times is excluded
// from future claims and the failure that reached the cap is logged at ERROR
// with the row's id, topic, type and key; the row STAYS in the outbox with
// its attempts for inspection until an operator re-arms it
// (UPDATE outbox SET attempts = 0, next_attempt_at = now() WHERE id = ...).
// One bad row must not stop the whole relay forever; that later rows then
// overtake it, per-key ordering included, is the accepted trade.
type OutboxRelay struct {
	pool        *pgxpool.Pool
	encoder     RelayEncoder
	sink        RelaySink
	logger      *slog.Logger
	interval    time.Duration
	batchSize   int
	maxAttempts int
	maxBackoff  time.Duration
}

// OutboxRelayOption configures an OutboxRelay beyond its required pool,
// encoder and sink.
type OutboxRelayOption func(*OutboxRelay)

// DefaultOutboxMaxAttempts is the attempt cap a composition root uses when
// OUTBOX_MAX_ATTEMPTS is unset (fleet convention). The relay's own zero value
// stays 0 (unlimited) so the cap is always an explicit WithMaxAttempts at the
// composition root (fitness test TestOutboxRelaysPassAttemptCap).
const DefaultOutboxMaxAttempts = 20

// DefaultOutboxMaxBackoff caps the exponential retry delay of a failing row.
const DefaultOutboxMaxBackoff = 5 * time.Minute

// AttemptCapReachedMsg is the ERROR log message written once for a row whose
// failure reached the attempt cap.
const AttemptCapReachedMsg = "outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with attempts = 0 after fixing the cause"

// DeadLetteredMsg is the WARN log message written for every poison row the
// relay moves to the dead-letter topic.
const DeadLetteredMsg = "outbox relay: dead-lettered a row the encoder refuses; it will never send, later rows overtake it"

// WithInterval overrides the default 1s poll interval (also the base of a
// failed row's retry backoff). Non-positive values are ignored.
func WithInterval(d time.Duration) OutboxRelayOption {
	return func(r *OutboxRelay) {
		if d > 0 {
			r.interval = d
		}
	}
}

// WithBatchSize overrides the default 100-row batch claimed per pass.
// Non-positive values are ignored.
func WithBatchSize(n int) OutboxRelayOption {
	return func(r *OutboxRelay) {
		if n > 0 {
			r.batchSize = n
		}
	}
}

// WithLogger overrides the default slog.Default().
func WithLogger(l *slog.Logger) OutboxRelayOption {
	return func(r *OutboxRelay) {
		if l != nil {
			r.logger = l
		}
	}
}

// WithMaxAttempts overrides the default of 0 (unlimited retries): a row that
// has failed this many times is excluded from future claims and logged at
// ERROR, instead of retried forever. Every cmd/* that builds a relay must
// pass it (architecture fitness test TestOutboxRelaysPassAttemptCap).
func WithMaxAttempts(n int) OutboxRelayOption {
	return func(r *OutboxRelay) {
		if n >= 0 {
			r.maxAttempts = n
		}
	}
}

// WithMaxBackoff overrides the 5m cap of a failed row's retry delay.
func WithMaxBackoff(d time.Duration) OutboxRelayOption {
	return func(r *OutboxRelay) {
		if d > 0 {
			r.maxBackoff = d
		}
	}
}

// NewOutboxRelay builds an OutboxRelay draining pool's outbox table onto
// sink, encoding every row through encoder.
func NewOutboxRelay(pool *pgxpool.Pool, encoder RelayEncoder, sink RelaySink, opts ...OutboxRelayOption) *OutboxRelay {
	r := &OutboxRelay{
		pool:       pool,
		encoder:    encoder,
		sink:       sink,
		logger:     slog.Default(),
		interval:   time.Second,
		batchSize:  100,
		maxBackoff: DefaultOutboxMaxBackoff,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RelayOnce runs exactly one claim-and-send pass and reports how many rows
// left the outbox (published or dead-lettered), for tests to drive
// deterministically; production uses Run's loop. Unlike Run it returns the
// pass's error — a send failure as an *outbox.SendFailure — so a test can
// assert on it without racing a background loop.
func (r *OutboxRelay) RelayOnce(ctx context.Context) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin outbox pass: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := r.claim(ctx, tx)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, tx.Commit(ctx)
	}

	relayed := 0
	for _, row := range rows {
		msgs, cause, ok := r.encoder.Encode(row.row)
		if !ok {
			if err := r.deadLetter(ctx, tx, row, cause); err != nil {
				return relayed, err
			}
			relayed++
			continue
		}
		if len(msgs) == 0 {
			// An encoder that encodes nothing for an encodable row is a
			// programming error: stop the pass (the row stays due) rather
			// than delete an unpublished occurrence.
			return relayed, fmt.Errorf("outbox relay: encoder returned no messages for row %d", row.id)
		}
		// One Send for every destination topic (the integration topic and
		// its analytics twin): either both land or the row is retried as a
		// whole, and the twin carries the same CloudEvents id, so a partial
		// retry is deduped downstream.
		if err := r.sink.Send(ctx, msgs...); err != nil {
			return relayed, r.recordFailure(ctx, tx, row, msgs[0], err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM outbox WHERE id = $1`, row.id); err != nil {
			return relayed, fmt.Errorf("delete outbox row %d: %w", row.id, err)
		}
		relayed++
	}
	if err := tx.Commit(ctx); err != nil {
		return relayed, fmt.Errorf("commit outbox pass: %w", err)
	}
	return relayed, nil
}

// Run polls until ctx is cancelled and then returns nil: a failed pass is
// logged and retried after the interval (the rows stay due, nothing is
// lost), and a pass that claimed a full batch runs again immediately, since
// more work is likely waiting. Run never dials anything itself: a broker
// that is down at boot just makes passes fail until it is up.
func (r *OutboxRelay) Run(ctx context.Context) error {
	for {
		n, err := r.RelayOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			r.logger.ErrorContext(ctx, "outbox relay pass failed", "error", err)
		}
		if n >= r.batchSize && err == nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(r.interval):
		}
	}
}

// claimedRow is one claimed outbox row.
type claimedRow struct {
	id  int64
	row outbox.Row
}

// claim locks up to batchSize due rows in id order: next_attempt_at due now
// and — when a cap is set — attempts below it. SKIP LOCKED keeps a second
// relay instance from racing this one.
func (r *OutboxRelay) claim(ctx context.Context, tx pgx.Tx) ([]claimedRow, error) {
	capFilter, args := "", []any{r.batchSize}
	if r.maxAttempts > 0 {
		capFilter = "AND attempts < $2"
		args = append(args, r.maxAttempts)
	}
	rows, err := tx.Query(ctx, `
		SELECT id, event_id, aggreg_type, aggreg_id, payload, occurred_at
		FROM outbox
		WHERE next_attempt_at <= now() `+capFilter+`
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("claim outbox rows: %w", err)
	}
	defer rows.Close()

	var batch []claimedRow
	for rows.Next() {
		var c claimedRow
		if err := rows.Scan(&c.id, &c.row.EventID, &c.row.AggregType, &c.row.AggregID, &c.row.Payload, &c.row.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan outbox row: %w", err)
		}
		batch = append(batch, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read outbox rows: %w", err)
	}
	return batch, nil
}

// deadLetter sends the poison row's dead-letter message and deletes the row
// in the same transaction: the row left through the DLQ, and later rows are
// free to overtake it. A dead-letter SEND failure (the broker is down) is
// returned as a plain error so the pass stops like any send failure: the row
// stays due and the next pass retries the dead-lettering.
func (r *OutboxRelay) deadLetter(ctx context.Context, tx pgx.Tx, row claimedRow, cause string) error {
	dlq := r.encoder.DeadLetter(row.row, cause)
	if err := r.sink.Send(ctx, dlq); err != nil {
		return r.recordFailure(ctx, tx, row, dlq, fmt.Errorf("dead-letter outbox row %d: %w", row.id, err))
	}
	if _, err := tx.Exec(ctx, `DELETE FROM outbox WHERE id = $1`, row.id); err != nil {
		return fmt.Errorf("delete dead-lettered outbox row %d: %w", row.id, err)
	}
	r.logger.WarnContext(ctx, DeadLetteredMsg,
		"outbox_id", row.id, "topic", dlq.Topic, "key", string(dlq.Key),
		"aggreg_type", row.row.AggregType, "event_id", row.row.EventID, "cause", cause)
	return nil
}

// recordFailure stages the failed row's attempts+1 and a pushed-back
// next_attempt_at, commits the pass (every row already relayed in it is
// deleted in the same tx) and returns the failure as an *outbox.SendFailure
// carrying the row's new attempt count, so the pass stops here and the next
// one retries this row first. The updates use a ctx that survives
// cancellation, so a shutdown mid-pass still persists what was relayed.
func (r *OutboxRelay) recordFailure(ctx context.Context, tx pgx.Tx, row claimedRow, msg outbox.Message, sendErr error) error {
	dctx := context.WithoutCancel(ctx)
	attempts := r.rowAttempts(dctx, tx, row)
	next := time.Now().Add(retryBackoff(r.interval, attempts, r.maxBackoff))
	failure := &outbox.SendFailure{RowID: row.id, Message: msg, Err: sendErr}
	if _, err := tx.Exec(dctx, `
		UPDATE outbox SET attempts = attempts + 1, next_attempt_at = $2 WHERE id = $1
	`, row.id, next); err != nil {
		return errors.Join(sendErr, fmt.Errorf("record outbox failure: %w", err))
	}
	if err := tx.Commit(dctx); err != nil {
		return errors.Join(sendErr, fmt.Errorf("commit outbox pass: %w", err))
	}
	failure.Attempts = attempts + 1
	r.logFailure(dctx, row, msg, failure.Attempts, sendErr)
	return failure
}

// rowAttempts reads the row's current attempts inside the pass (a prior
// failed pass may have raised it since it was claimed fresh here, but the
// row is locked, so this is the authoritative count for the backoff).
func (r *OutboxRelay) rowAttempts(ctx context.Context, tx pgx.Tx, row claimedRow) int {
	var attempts int
	if err := tx.QueryRow(ctx, `SELECT attempts FROM outbox WHERE id = $1`, row.id).Scan(&attempts); err != nil {
		r.logger.ErrorContext(ctx, "outbox relay: read attempts failed", "outbox_id", row.id, "error", err)
	}
	return attempts
}

// logFailure logs a failed send at WARN while the row will be retried, and
// at ERROR once it reached the attempt cap: from the next pass on it is
// excluded from claims and stays in the outbox until an operator re-arms it.
func (r *OutboxRelay) logFailure(ctx context.Context, row claimedRow, msg outbox.Message, attempts int, sendErr error) {
	if r.maxAttempts > 0 && attempts >= r.maxAttempts {
		r.logger.ErrorContext(ctx, AttemptCapReachedMsg,
			"outbox_id", row.id, "topic", msg.Topic, "event_type", msg.EventType,
			"key", string(msg.Key), "attempts", attempts, "max_attempts", r.maxAttempts, "error", sendErr)
		return
	}
	r.logger.WarnContext(ctx, "outbox relay: send failed, stopping this pass at the failed row",
		"outbox_id", row.id, "topic", msg.Topic, "event_type", msg.EventType,
		"key", string(msg.Key), "attempts", attempts, "error", sendErr)
}

// retryBackoff spaces a failing row's retries exponentially from the relay
// interval (1s, 2s, 4s ... capped), so a broker outage is retried gently
// instead of on every poll.
func retryBackoff(base time.Duration, attempts int, max time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if attempts < 0 {
		attempts = 0
	}
	d := base
	for range min(attempts, 16) {
		d *= 2
		if d >= max {
			return max
		}
	}
	return min(d, max)
}
