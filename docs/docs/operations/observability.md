---
id: observability
title: Observability
sidebar_label: Observability
sidebar_position: 3
---

# Observability

What signals the service emits, where they go, and what is deliberately not there. The decision is
[ADR 0011](/docs/adr/0011-adopt-the-fleet-observability-standard): the fleet observability standard,
Tier 1 (identical everywhere) plus one Tier 2 business counter.

## Export

Every long-running binary (`cmd/api`, `cmd/mcp`, `cmd/a2a`, `cmd/returns-projector`,
`cmd/returns-reports`) calls `telemetry.Setup` at boot. Traces and metrics are pushed over OTLP/gRPC
to the Collector; metrics are pushed every 15 seconds. Export is lazy and non-blocking: with no
Collector listening the service starts and serves normally and the telemetry is dropped. An unset or
empty endpoint is not "off": it falls back to `localhost:4317`. A bare `host:port` is treated as
plaintext; pass an `https://` URL for TLS.

| Variable | Default | Meaning |
| --- | --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | Collector OTLP/gRPC endpoint, `host:port` or URL |
| `OTEL_SERVICE_NAME` | `returns-processing` (api), `returns-processing-mcp`, `returns-processing-a2a`, `returns-projector`, `returns-reports` | `service.name` of every span and metric |
| `SERVICE_VERSION` | `dev` | `service.version` |
| `ENVIRONMENT` | `local` | `deployment.environment.name` |

The chart renders all four for every component from `otel.*`, `serviceVersion` and `environment`
(see [Configuration](/docs/operations/configuration)).

## Metrics

| Instrument | Type, unit | Where | Series in Prometheus |
| --- | --- | --- | --- |
| `http.server.request.duration` | histogram, `s` | `cmd/api`, `cmd/mcp`, `cmd/a2a`, `cmd/returns-reports` (the chi routers), labelled by the route pattern (`http.route`), never the raw path | `http_server_request_duration_seconds_{bucket,sum,count}` |
| `returns.authorizations.decided` | counter, `{authorization}` | `cmd/api`, one count per `POST /returns` decision | `returns_authorizations_decided_total` |
| Go runtime metrics | contrib `runtime` instrumentation | every binary | as the contrib package names them |

The series names are the ones the OTel Collector's Prometheus exporter produces by default (dots to
underscores, unit suffix, `_total` for counters); the Collector configuration and dashboards are not
part of this repository, so how they are rendered there is not evidenced here.

`returns.authorizations.decided` has two attributes and no others:

- `outcome`: `authorized` or `rejected`.
- `reason`, on `rejected` only: the problem slug the REST adapter answers with for the refusal:
  `return-not-authorizable` (line not allocated, not manifested, window expired, over the policy
  cap, or no policy declared), `open-rma-exists`, `concurrent-modification`, `invalid-quantity`,
  `malformed-request-body`, `internal-error`. A rising `return-not-authorizable` or `open-rma-exists`
  means clients ask for returns the rules refuse; a rising `internal-error` or
  `concurrent-modification` means the service itself is in trouble.

A request the REST adapter rejects before the use case runs (a body that is not valid JSON, a missing
`Idempotency-Key`) is not a decision and is not counted. An idempotent replay answers from the stored
response and does not run the use case again.

`GET /metrics` does not exist: nothing is scraped from the service.

## Traces

Every chi router starts a server span per request, named after the route pattern (never the raw
path, which carries ids), through `otelchi`; the W3C `traceparent` header of an incoming request is
honoured. The A2A adapter's own `otel.Tracer` spans now have a real provider behind them.

**Not propagated across Kafka.** The `outbox` table has no `headers` column, so the relay publishes
only the CloudEvents `content-type` header: no `traceparent`/`tracestate` leaves with an event, and a
consumer's span starts a new trace. Adding it needs a migration and is not part of
[ADR 0011](/docs/adr/0011-adopt-the-fleet-observability-standard).

## Logs

One JSON line per event (`slog`, stdout), level via `LOG_LEVEL`. A record written with a context that
carries a span (the `*Context` slog calls, which the request log uses) also carries `trace_id` and
`span_id`; a record written outside a request (boot, shutdown, the relay and the consumers) carries
neither.

### HTTP

One `http request` line per request on the REST, MCP, A2A and reports routers: `method`, `route` (the
matched pattern, `unmatched` when none matched), `status`, `duration_ms`, `bytes` and `request_id`. The
level follows the status: `5xx` is ERROR, `4xx` is WARN, everything else INFO. `/healthz` and
`/readyz` are not logged. Errors are problem documents; the slug is in the response, not the log line.

### Boot and lifecycle

- `telemetry configured` (service_name, otlp_endpoint) — after `Setup`, before the router is built.
- `returns-processing running` / `http server listening` (addr) — after migrations and the pool; boot
  problems are one aggregated `invalid configuration: ...` error naming every missing variable, then
  exit 1.
- readiness flips: `not ready: postgres` / `not ready: kafka` behind `503 /readyz`.
- shutdown: drain delay, then HTTP closed, then consumers + relay drained, then the telemetry flush
  (bounded to 5 seconds; a failed flush is a WARN, never a non-zero exit); a consumer death surfaces
  as `service exited with error` and a non-zero exit (the pod restarts).

### Outbox relay

- `outbox relay: dead-lettered a row the encoder refuses; it will never send, later rows overtake
  it` (WARN) — one per poison row, on `warehouse.returns-processing.events.dlq`.
- `outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with
  attempts = 0 after fixing the cause` (ERROR) — the row's id, topic, type and key are attached.

### Consumers

- WARN `... not a valid CloudEvent ...` + commit-past — a flat envelope or garbage on a consumed
  topic; never retried, never blocks the partition.
- Unknown-type skips are logged at DEBUG/INFO.
- A poison payload (can never apply) is retried a bounded number of times, then dead-lettered with
  its cause in the `x-dlq-*` headers.

## Health endpoints

| Endpoint | Answers | Kubernetes use |
| --- | --- | --- |
| `GET /healthz` | `200` while the process runs | liveness probe |
| `GET /readyz` | `200` ready / `503 {"status":"not_ready"}` (draining) or `503 dependency-unavailable` (Postgres ping or Kafka metadata fails; cached a few seconds) | readiness probe |

`cmd/mcp`, `cmd/a2a` and `cmd/returns-reports` serve their own open `GET /healthz` (JSON
`{"status":"ok"}`); `cmd/returns-projector` serves `/healthz` and `/readyz` on its admin port.

## Suggested alerts

Guidance, not a shipped configuration: the `5xx` share and p95 of `http.server.request.duration`
per route, the `internal-error` and `concurrent-modification` share of
`returns.authorizations.decided`, pod restarts (a consumer death is a deliberate crash-restart), the
`503` rate on `/readyz` (dependency outages), and — via the database — `SELECT count(*) FROM outbox`
growth (relay cannot drain) and capped rows (`attempts >= 20`).

## Build-time quality gates

Not runtime observability, but the verification the repo actually carries: 90 % coverage gate on
domain + application, mutation thresholds 95/95 per domain package (measured 100/100), the BDD
scenarios under `features/`, golden bytes per published event type, catalogue + hexagonal fitness tests, Spectral on
both API specs, and the docs-api-drift CI job that regenerates this site's REST reference from the
spec.
