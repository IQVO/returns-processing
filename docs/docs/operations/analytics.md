---
id: analytics
title: Analytics read side
sidebar_label: Analytics
sidebar_position: 4
---

# Analytics read side

The returns data product: a **separate analytical database** fed by a
**projector** from this service's own analytics topic, and a **read-only
reports** binary over it ([ADR 0007](/docs/adr/0007-analytics-read-side)).
Disabled by default (`analytics.enabled: false` in the chart); nothing about
the OLTP service changes when it is off.

## How it flows

```
domain event ──▶ outbox (1 tx) ──▶ relay, ONE pass, TWO messages:
                   │                 warehouse.returns-processing.events    (integration)
                   └─────────────▶   warehouse.returns-processing.analytics (twin, same id,
                                     same payload, only the dataschema differs)
                                            │
                     cmd/returns-projector  │ fixed group ANALYTICS_CONSUMER_GROUP
                     (the ONLY writer)      ▼
                     analytical Postgres (own DSN, own migrations, facts only)
                                            │
                     cmd/returns-reports    │ read-only pool
                     (:8092)                ▼
                     GET /reports/{disposition-mix,value-recovery,return-cycle-time,freshness}
```

Five of the seven event types fold into the model (`ReturnReceiptOpened`,
`UnexpectedReturnReceived`, `ReturnLineInspected`, `DispositionDecided`,
`ReturnLineStaged`); the two authorization twins are ignored by the projector
(v1 answers the disposition questions). Deterministic poison is dead-lettered
to `warehouse.returns-processing.analytics.dlq`.

## The reports

All four accept optional `from`/`to` (RFC 3339; both omitted = the last 30
days; at most 366 days; `from` inclusive, `to` exclusive), an optional
`siteId`, and `granularity=day`. Errors are RFC 7807 problems
(`invalid-report-range`, `invalid-report-granularity`). No auth: the
in-cluster boundary is the access control (fleet rule).

| endpoint | answers | rows |
| --- | --- | --- |
| `GET /reports/disposition-mix` | how the outcomes split per policy version | `policy_version`, `outcome`, `lines`, `units` |
| `GET /reports/value-recovery` | restocked lines and units per site per day | `day`, `sites[] {site_id, restocked_lines, restocked_units}` |
| `GET /reports/return-cycle-time` | receipt-opened→staged latency percentiles per day | `day`, `staged_lines`, `cycle_time {count, p50_seconds, p95_seconds}` |
| `GET /reports/freshness` | how far the projection is behind | `as_of`, `lag_seconds` (both `null` before the first event) |

Example:

```console
$ curl 'localhost:8092/reports/return-cycle-time?from=2026-10-20T00:00:00Z&to=2026-10-22T00:00:00Z'
{"from":"2026-10-20T00:00:00Z","to":"2026-10-22T00:00:00Z","granularity":"day",
 "days":[{"day":"2026-10-21","staged_lines":2,
          "cycle_time":{"count":2,"p50_seconds":411,"p95_seconds":648}}]}
```

## Operating it

Two extra workloads behind `analytics.enabled` (default off; the chart
refuses to render it without `analytics.url`/`analytics.existingSecret` and
`analytics.consumerGroup` — the binaries exit at boot otherwise):

- **`cmd/returns-projector`** — the analytical database's ONLY writer. Env:
  `ANALYTICS_DATABASE_URL` (DIRECT DSN, never through PgBouncer transaction
  pooling: the boot migration takes an advisory lock), `KAFKA_BROKERS`,
  `ANALYTICS_CONSUMER_GROUP`, `ADMIN_ADDR` (default `:8091`). Serves
  `/healthz` + `/readyz` only; a dead consumer loop exits non-zero so the
  pod restarts instead of silently consuming nothing.
- **`cmd/returns-reports`** — read-only reader. Env: `ANALYTICS_DATABASE_URL`
  (ideally a read-only role), `HTTP_ADDR` (default `:8092`), `LOG_LEVEL`.
  Never dials Kafka; writes nothing.

Both ship in the same image as `cmd/api` (the Dockerfile builds every
`cmd/*`); the analytical migrations are embedded in the binaries.

### Rebuilding the model

The analytical database is a derived copy: drop it, recreate it empty, and
run the projector under a NEW consumer group — it replays from the earliest
retained offset and the facts rebuild (milestones are earliest-wins, so
arrival order never matters). Do not rename a running group's id: a rename
IS a new group and re-reads the topic from the start.

### Freshness

`GET /reports/freshness` is the projection's heartbeat: `lag_seconds` is
`now − the newest applied event time`. A growing lag with a healthy projector
pod means the topic is quiet, not broken; a growing lag with restarts means
poison (check the DLQ topic) or the analytical database.
