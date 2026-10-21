# ADR 0005: the transactional outbox and its relay — events leave through the outbox only

## Status

Accepted (2026-10-21).

## Context

Every state change in this context (an authorized RMA, an opened receipt, an
inspection grade, a decided or overridden disposition) publishes CloudEvents
to `warehouse.returns-processing.events` (ADR 0004). Writing the aggregate and
publishing as two independent operations is unsound in both directions: an
event published for a decision that rolled back is a phantom, and a committed
decision whose publish failed is invisible downstream. The fleet rule
(`.claude/rules/fleet/idempotency-and-outbox.md`) therefore mandates the
transactional outbox pattern, and this ADR records how it is built here — the
last of the five foundational decisions (context, aggregates, local copies,
envelope, outbox).

## Decision

### The write side: one transaction, aggregate plus outbox rows

A command's unit of work (`internal/application/usecases/uow.go`) persists the
aggregate AND inserts already-encoded rows into the `outbox` table in the SAME
Postgres transaction (the `ports.EventPublisher` side of
`internal/application/outbox`). The CloudEvents `id` is a UUID minted ONCE at
encode time and stored with the row, so every relay attempt and every
redelivery after a crash carries the same `id` — that is the anchor consumers
dedupe on. The row carries the topic, the Kafka key (= the subject), the full
JSON envelope and its `next_attempt_at`. There is no second write path: the
publisher only ever inserts, and nothing else in the process publishes.

### The relay: the only path from outbox to broker

`postgres.OutboxRelay` (in-process, started by `cmd/api`) polls the outbox
every `OUTBOX_RELAY_INTERVAL` (default 1 s). One pass claims up to 100 due
rows under `FOR UPDATE SKIP LOCKED` — two overlapping relays never claim the
same row — and walks them in id order, one row at a time, so a later event
never overtakes an earlier one of the same aggregate that is still being
retried:

- **Sendable row**: sent through one acks-all Kafka writer
  (`internal/adapters/outbound/kafka/relay_sink.go`), then DELETED in the same
  transaction. Delivery is at-least-once: a crash between send and commit
  republishes the row with the SAME persisted event id, which is exactly what
  lets consumers dedupe.
- **Poison row** — one the encoder refuses (unknown event, malformed payload;
  the contract is `Encode`'s `ok=false` plus a cause — is dead-lettered to
  `warehouse.returns-processing.events.dlq` and then deleted. It is
  deterministic, no retry can fix it, and keeping it would block every later
  row behind it. The dead letter carries the row and the cause.
- **Send failure** (broker down): the row stays with `attempts + 1` and a
  pushed-back `next_attempt_at` (exponential backoff from the poll interval,
  capped at 5 minutes), the pass stops, later rows stay unpublished, and the
  next pass retries in id order.

### The attempt cap: one bad row must not stop the relay forever

With `OUTBOX_MAX_ATTEMPTS` (default 20, `0` = unlimited), a row that has
failed that many times is excluded from future claims; the failure that
reached the cap is logged at ERROR with the row's id, topic, type and key, and
the row STAYS in the outbox with its attempts for inspection until an
operator re-arms it:

```sql
UPDATE outbox SET attempts = 0, next_attempt_at = now() WHERE id = '<uuid>';
```

Later rows then overtake it — per-key ordering included — which is the
accepted trade: the fleet's `TestOutboxRelaysPassAttemptCap` fitness test
fails any composition root that forgets to pass the cap.

### Who runs it

Only `cmd/api` builds a relay. `cmd/mcp` wires no write use case at all, so it
can never insert into the outbox and never starts a relay; the tests drive the
relay against real Kafka via testcontainers (`relay_integration_test.go`,
`probe_integration_test.go`), and the golden-bytes contract
(`relay_sink.go` + `internal/adapters/outbound/kafka/testdata/`) pins one
exact envelope per published type.

## Consequences

- A consumer never sees an event for an uncommitted decision, and a committed
  decision always eventually leaves the service while Kafka is reachable.
- Delivery is at-least-once with a stable id per occurrence; consumers MUST
  dedupe on the CloudEvents `id` (ADR 0003 states the same rule for what this
  service consumes).
- Per-aggregate ordering holds among rows that send successfully; a row that
  hits the attempt cap is overtaken, deliberately, after 20 failures.
- The outbox table is operational state, not archive: successful rows are
  deleted, poison rows leave for the DLQ, and only capped rows (and their
  backlog) persist. Housekeeping guidance is on the docs Runbook.

## References

- Fleet rule: `.claude/rules/fleet/idempotency-and-outbox.md`.
- Precedent: pick-face-replenishment ADR 0005 (evaluation semantics and
  persistence) and inbound-receiving ADR 0007 (outbox relay attempt cap).
