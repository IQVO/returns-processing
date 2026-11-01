---
id: runbook
title: Runbook
sidebar_label: Runbook
sidebar_position: 2
---

# Runbook

How to deploy, migrate and operate returns-processing.

## Deployment status

Checked 2026-10-21:

- The service and the packaging (Helm chart, Dockerfile, CI jobs) are **merged to `develop`** (PRs
  #2 and #3).
- `warehouse-infra` does **not** reference returns-processing yet: no Terraform `local.services`
  entry, no helm-values file. First deployment needs that registration.
- **No release has been cut**: the first merge to `main` runs `docker-publish` + `release`. Work
  through `docs/before-first-release.md` on a feature branch before that first `develop` → `main`
  PR (row 10 there is this docs site).

## What a deployment needs

- Postgres reachable at `DATABASE_URL` (and, behind PgBouncer, a direct session DSN in
  `MIGRATIONS_DATABASE_URL`).
- Kafka reachable at `KAFKA_BROKERS`. Topics are **not** declared anywhere: the broker
  auto-creates them on first write (the relay's writer and the dead-letter writers set
  `AllowAutoTopicCreation`).
- The three consumer group ids, stable and service-prefixed
  ([Configuration](/docs/operations/configuration)).
- No auth anywhere: access control is the network boundary.

### Readiness and shutdown

`/readyz` = drain gate AND Postgres ping AND Kafka metadata (reachability only), cached a few
seconds. `/healthz` is liveness only. Shutdown order (all in `cmd/api`): flip `/readyz` off →
`SHUTDOWN_DRAIN_DELAY` (default 5 s) → close HTTP → cancel and drain the consumers and the relay
(10 s each bound). A consumer that dies for any reason but a clean cancel is a **process death**:
`run` returns the consumer's error, the pod exits non-zero and restarts — a half-alive service on
frozen local copies is worse than a blip. The chart's `terminationGracePeriodSeconds` must stay
above the sum of those bounds (asserted by the chart tests).

## Migrations

Embedded and idempotent (golang-migrate), run at boot by BOTH binaries — `cmd/mcp` can boot against
a fresh database before `cmd/api` has. The advisory lock makes concurrent starts safe; behind
PgBouncer set `MIGRATIONS_DATABASE_URL` (session DSN), which the runtime pool never uses.
`0001_returns-processing` creates the OLTP schema (aggregates, local copies, `outbox`,
`processed_events`); `0002_idempotency_keys` creates the REST replay store. There is no
down-migration procedure in production — schema changes ship as new forward migrations.

## Kafka topics and consumer groups

| Topic | Direction | Group (env) |
| --- | --- | --- |
| `warehouse.returns-processing.events` | produced (outbox relay) | — |
| `warehouse.returns-processing.events.dlq` | produced (outbox poison rows) | — |
| `warehouse.product-master.events` (+ `.dlq`) | consumed | `PRODUCT_CLASSIFICATION_CONSUMER_GROUP` |
| `warehouse.order-management.events` (+ `.dlq`) | consumed | `ORDER_LINES_CONSUMER_GROUP` |
| `warehouse.fulfillment.events` (+ `.dlq`) | consumed | `MANIFEST_CONSUMER_GROUP` |

Do not rename a group id in a running environment: a new id re-reads the topic from the start
(idempotent effects, but a full replay).

### Delivery semantics of the consumers

`FetchMessage` + effect + `CommitMessages` only after success. The CloudEvents `id` is claimed in
the SAME transaction as the effect (`processed_events`), so a redelivery changes nothing and still
commits. An unknown type commits past untouched; a non-CloudEvent (e.g. a retired flat envelope) is
WARN-logged and skipped; a payload that can never apply (e.g. a blank SKU) is poison: bounded
retry, then dead-letter, and the message behind it still applies.

### Dead-letter topics

Each consumed topic has its `<topic>.dlq`: a poison message lands there verbatim with `x-dlq-*`
headers naming the consumer and the cause, after a bounded retry. Producer-side, outbox rows the
encoder refuses go to `warehouse.returns-processing.events.dlq`. A dead letter is never retried
automatically; it is an operator input.

## Outbox relay

One pass every `OUTBOX_RELAY_INTERVAL` (default 1 s) claims up to 100 due rows `FOR UPDATE SKIP
LOCKED` and walks them in id order: send (acks=all) + delete on success; dead-letter + delete for a
row the encoder refuses; on a send failure the row keeps `attempts + 1` and a backed-off
`next_attempt_at` (cap 5 min) and the pass stops. After `OUTBOX_MAX_ATTEMPTS` (default 20)
failures a row is excluded from claims, logged at ERROR with its id, topic, type and key, and
STAYS for inspection; re-arm it after fixing the cause:

```sql
UPDATE outbox SET attempts = 0, next_attempt_at = now() WHERE id = '<uuid>';
```

Full design: [ADR 0005](/docs/adr/0005-transactional-outbox-and-relay).

## Housekeeping

- `outbox`: drained by the relay (successful rows deleted). Only capped rows and a broker outage
  grow it. **No retention sweeper exists** (roadmap); watch the table size during a long outage.
- `processed_events` (consumer dedupe claims) and `idempotency_keys` (REST replays): grow forever.
  No retention policy yet — the same roadmap item as the fleet's other contexts.

## Routine procedures

- **Declare a new policy version** — `PUT /disposition-policies/{n}`; nothing re-decides old
  dispositions (they keep their `policy_version`), new inspections use the new version immediately.
- **Fix a wrong disposition** — `POST /dispositions/{id}/overrides` with a reason; never re-inspect.
- **Re-arm a capped outbox row** — see above.
- **Inspect the DLQs** — `<consumed topic>.dlq` (inbound poison; headers carry the cause) and
  `warehouse.returns-processing.events.dlq` (producer-side).

## Scaling notes

One process does everything (REST + consumers + relay); scale by replicas — the SKIP LOCKED claim
keeps relays from double-claiming, the id-dedupe claim keeps consumer effects idempotent, and
aggregate versions keep writes safe. Kafka key = aggregate id, so per-aggregate order holds within a
partition. `cmd/mcp` scales independently (it only reads).
