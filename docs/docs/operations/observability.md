---
id: observability
title: Observability
sidebar_label: Observability
sidebar_position: 3
---

# Observability

What signals exist today, and what deliberately does not.

## Metrics

**None.** The service exports no runtime metrics: no Prometheus registry, no `/metrics` handler, no
OpenTelemetry meters anywhere in `cmd/` or `internal/`. The spec used to declare `GET /metrics`; it
was dropped in the REST guideline cutover ([ADR 0009](/docs/adr/0009-adopt-the-fleet-rest-api-guideline))
so `apis/openapi.yaml` matches the router in `internal/adapters/inbound/http/server.go`. Adding the
exposition later is a new decision — until then, the only runtime signals are logs and the two
probes.

## Traces

No tracing SDK is wired. Outgoing Kafka messages MAY carry W3C `traceparent`/`tracestate` headers
when the writing request provided them (the CloudEvents helper passes headers through), but this
service starts no spans of its own.

## Logs

One JSON line per event (`slog`, stdout), level via `LOG_LEVEL`. The lifecycle messages that
matter:

### Boot and lifecycle

- `api: listening` (addr) — after migrations and the pool; boot problems are one aggregated
  `invalid configuration: ...` error naming every missing variable, then exit 1.
- readiness flips: `not ready: postgres` / `not ready: kafka` behind `503 /readyz`.
- shutdown: drain delay, then HTTP closed, then consumers + relay drained; a consumer death
  surfaces as `service exited with error` and a non-zero exit (the pod restarts).

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

### HTTP

Every request logs method, path, status and duration at DEBUG. Errors are problem documents; the
slug is in the response, not the log line.

## Health endpoints

| Endpoint | Answers | Kubernetes use |
| --- | --- | --- |
| `GET /healthz` | `200` while the process runs | liveness probe |
| `GET /readyz` | `200` ready / `503 {"status":"not_ready"}` (draining) or `503 dependency-unavailable` (Postgres ping or Kafka metadata fails; cached a few seconds) | readiness probe |

`cmd/mcp` serves its own open `GET /healthz` (JSON `{"status":"ok"}`).

## Suggested alerts (when metrics exist)

Until the metrics exposition is built, alert on what exists today: pod restarts (a consumer death
is a deliberate crash-restart), the `503` rate on `/readyz` (dependency outages), and — via the
database — `SELECT count(*) FROM outbox` growth (relay cannot drain) and capped rows
(`attempts >= 20`). This list is guidance, not a shipped configuration.

## Build-time quality gates

Not runtime observability, but the verification the repo actually carries: 90 % coverage gate on
domain + application, mutation thresholds 95/95 per domain package (measured 100/100), 15 BDD
scenarios, golden bytes per published event type, catalogue + hexagonal fitness tests, Spectral on
both API specs, and the docs-api-drift CI job that regenerates this site's REST reference from the
spec.
