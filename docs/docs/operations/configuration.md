---
id: configuration
title: Configuration
sidebar_label: Configuration
sidebar_position: 1
---

# Configuration

All configuration is environment variables, read once at boot; a missing mandatory variable is a
boot error naming it (every problem is reported at once, before a connection opens or the listener
binds). There is no config file and no `*_MODE` toggle anywhere.

## `cmd/api`

### Mandatory

| Variable | Meaning |
| --- | --- |
| `DATABASE_URL` | Postgres DSN (the pool; in-cluster this is the PgBouncer-facing DSN) |
| `KAFKA_BROKERS` | comma-separated broker addresses |
| `PRODUCT_CLASSIFICATION_CONSUMER_GROUP` | group id of the product-master classifications consumer (`warehouse.product-master.events`) |
| `ORDER_LINES_CONSUMER_GROUP` | group id of the order lines consumer (`warehouse.order-management.events`) |
| `MANIFEST_CONSUMER_GROUP` | group id of the manifests consumer (`warehouse.fulfillment.events`) |

The three group ids are read (and required) at boot even though they are spent by the consumers:
a deployment missing one fails fast, not at the step that first uses it. They are stable,
service-prefixed names — never derived from the pod — so a rescheduled pod resumes from its
committed offsets.

### Optional

| Variable | Default | Meaning |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | REST listen address |
| `OUTBOX_RELAY_INTERVAL` | `1s` | relay poll interval (Go duration or plain seconds); also the base of a failed row's retry backoff |
| `OUTBOX_MAX_ATTEMPTS` | `20` | failures before the relay excludes a row from claims; `0` = unlimited; a typo falls back to the default, never silently uncaps |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | wait after flipping `/readyz` before closing the listener; `0` disables |
| `LOG_LEVEL` | `info` | `debug` \| `info` \| `warn`/`warning` \| `error` (case-insensitive) |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | direct session DSN for the boot migrations — set it when `DATABASE_URL` points at PgBouncer, whose transaction pooling cannot honour golang-migrate's session-scoped advisory lock |

### Readiness

`/readyz` is the drain gate AND a Postgres ping AND a Kafka metadata check (reachability only,
never topics — the broker auto-creates topics on first write), cached for a few seconds. `/healthz`
is liveness only.

## `cmd/mcp`

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | — | **required**; refuses to boot without it (no in-memory mode) |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | as above; this binary also runs the idempotent boot migrations so it can start before `cmd/api` |
| `MCP_ADDR` | `:8090` | Streamable HTTP listen address (serves `/mcp` and `/`, open `GET /healthz`) |
| `LOG_LEVEL` | `info` | as above |

It wires **no** write use case, no outbox, no relay, no consumers and no Kafka client at all.

## `cmd/a2a`

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | — | **required**; refuses to boot without it (no in-memory mode) |
| `A2A_AGENT_URL` | — | **required**; absolute http(s) URL of the JSON-RPC endpoint, advertised on the Agent Card (the chart defaults it to the in-cluster Service DNS name, override with `a2a.agentUrl`) |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | as for `cmd/mcp` |
| `A2A_ADDR` | `:8093` | listen address (serves JSON-RPC at `/` and `/a2a`, the card at `/.well-known/agent-card.json`, open `GET /healthz`) |
| `LOG_LEVEL` | `info` | as above |

Read-only like `cmd/mcp`: no write use case, no outbox, no relay, no consumers, no Kafka client
([ADR 0008](/docs/adr/0008-a2a-inbound-adapter)).

## Fixed values (not configurable)

| Value | What |
| --- | --- |
| Topic `warehouse.returns-processing.events` (+ `.dlq` for producer-side dead letters) | the integration stream; auto-created on first write |
| Consumed topics `warehouse.{product-master,order-management,fulfillment}.events` (+ `.dlq`) | pinned in ADR 0003 |
| Relay batch size 100, backoff cap 5 min | `postgres.NewOutboxRelay` defaults (no env) |
| `content-type: application/cloudevents+json; charset=UTF-8` | every Kafka message |
| No auth on REST, MCP or A2A | fleet decision 2026-09-11, fitness-tested |

## Helm chart

`charts/returns-processing` renders everything above from values (chart tests refuse an empty
mandatory value):

```yaml
config:
  httpAddr: ":8080"            # HTTP_ADDR
  logLevel: "info"             # LOG_LEVEL
  outboxRelayInterval: ""      # empty = not rendered; binary default 1s
  outboxMaxAttempts: 20        # OUTBOX_MAX_ATTEMPTS
  shutdownDrainDelay: ""       # empty = not rendered; binary default 5s
  productClassificationConsumerGroup: "returns-processing-product-classification"
  orderLinesConsumerGroup: "returns-processing-order-lines"
  manifestConsumerGroup: "returns-processing-manifest"

database:
  url: ""                      # OR existingSecret (+ existingSecretKey DATABASE_URL) — MANDATORY
  migrationsExistingSecretKey: "MIGRATIONS_DATABASE_URL"   # optional key in the same secret

kafka:
  brokers: []                  # MANDATORY

mcp:
  enabled: false               # the read-only MCP Deployment + Service, off by default

a2a:
  enabled: false               # the read-only A2A Deployment + Service (port 8093), off by default
  agentUrl: ""                 # A2A_AGENT_URL; empty = the in-cluster Service DNS name
```

The chart refuses any `*_MODE` value, mounts the env ConfigMap via `envFrom`, keeps `DATABASE_URL`
in a Secret, and its pod hardening + grace period are asserted by
`charts/returns-processing/tests/` (`render_test.sh`, `test_service_selectors.py`).
