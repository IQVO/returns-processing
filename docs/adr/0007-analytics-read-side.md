# ADR 0007: Analytics read side: an analytics topic, a projector and the returns reports over a separate analytical database

## Status

Accepted (2026-10-21). This ADR fills the analytics read side that
[ADR 0004](0004-cloudevents-envelope-and-type-catalogue.md) reserved and that
the AsyncAPI described as "planned, not built". Additive: the OLTP API, the
integration topic `warehouse.returns-processing.events` and its payloads are
unchanged (the integration golden tests pass unchanged). No OLTP migration is
needed. Adds a second, separate database.

## Context

Every OLTP bounded context of the fleet ships an analytics read side: a
separate analytical Postgres fed by a **projector** that consumes the context's
own analytics topic, and a read-only **reports** binary over that database
(pick-face-replenishment ADR 0008 — the reference implementation this one
mirrors — slotting-optimization ADR 0006, inbound-receiving, product-master,
...). Until now this service published nothing analytical: its seven event
types went to the integration topic only, so any question about the reverse
flow had to be answered by grepping the OLTP read model, which keeps current
state, not history.

The questions the data product answers:

- **Disposition mix**: how do the disposition outcomes split per policy
  version (did policy v3 really restock more than v2 did)? Overrides are
  decisions too — they land in the mix under the policy version in force and
  their own disposition id.
- **Value recovery**: how many lines and units were restocked (staged for
  put-away) per site per day?
- **Return cycle time**: how long does a returned line take from receipt
  opened to staged, as percentiles per day?

## Decision

### 1. A dedicated analytics topic, written by the same outbox pass

Every domain event is published TWICE from the SAME outbox row: the
integration message on `warehouse.returns-processing.events` and its
**analytics twin** on `warehouse.returns-processing.analytics`, with the SAME
CloudEvents `id`, `type`, `subject` and Kafka key and a byte-identical
payload. Only the `dataschema` stream segment differs:
`urn:warehouse:returns-processing:analytics:<EventName>:v1` (the fleet
pattern, ADR 0004's envelope). The relay encoder maps one claimed outbox row
to both messages in ONE `Send`, so a partial failure retries the row as a
whole and the twin's shared `id` dedupes it downstream. All seven types are
twinned; there is no analytics-only event in v1. The topic is NOT an
integration contract: the only consumer is this service's own projector, and
other contexts keep reading the events topic.

### 2. What is projected

Five of the seven types fold into the model: `ReturnReceiptOpened` (per
carried line), `UnexpectedReturnReceived`, `ReturnLineInspected`,
`DispositionDecided` and `ReturnLineStaged`. The two authorization twins
(`ReturnAuthorized`, `ReturnAuthorizationRejected`) answer no v1 report and
are acknowledged and committed past (see "What is deliberately NOT reported").
Dead-letter topic: `warehouse.returns-processing.analytics.dlq`.

### 3. The analytical model

Facts, never aggregates, so any window can be recomputed
(`analytics/migrations/0001_returns_analytics`, a directory SEPARATE from the
OLTP `migrations/` and embedded separately):

- `analytics_processed_events`: the dedupe set (the CloudEvents `id`) and the
  freshness source (`max(occurred_at)`).
- `disposition_facts`, one row per `DispositionDecided` (an override is its
  own row under its own disposition id): the receipt line, grade, outcome,
  `policy_version`, supersedes/reason, `decided_at`. These are the
  disposition-mix cells.
- `return_line_facts`, one row per receipt LINE the projection has seen
  (keyed `(receipt_id, line_no)`), one milestone column per lifecycle step:
  `opened_at` (the receipt opening or the unexpected-line event), 
  `inspected_at`, `decided_at` (earliest decision), `staged_at`. Each
  milestone keeps the CloudEvents `time` of the EARLIEST such event seen
  (`LEAST`), and the descriptive columns keep the first non-null value, so
  arrival order never matters.

### 4. The projector (`cmd/returns-projector`)

- Consumes the analytics topic under a **fixed consumer group read from env**
  (`ANALYTICS_CONSUMER_GROUP`, required, no default — like the three OLTP
  consumers). At-least-once; a brand-new group starts at the earliest
  retained offset, which is the backfill.
- **Dedupe and effect in one transaction**: `Projection.Apply` validates the
  event, inserts the CloudEvents `id` into `analytics_processed_events`
  (`ON CONFLICT DO NOTHING`) and, only if it was inserted, folds the event
  into the fact tables in ONE analytical-database transaction. The Kafka
  offset is committed only after `Apply` returned (FetchMessage +
  CommitMessages, the fleet rule).
- **Poison policy**: a message that is not a valid CloudEvent 1.0 envelope:
  skipped with a WARN and committed past; a valid CloudEvent of a type the
  projector ignores (the two authorization twins): acknowledged; a known type
  whose payload fails validation or contradicts its envelope, or one the
  store deterministically rejects (`report.ErrRejected`, Postgres SQLSTATE
  class 22/23): **dead-lettered at once** (raw bytes plus the `x-dlq-*`
  headers) and committed past; a failed DLQ write retries so poison is never
  lost; a **transient** failure is retried with capped exponential backoff
  and never dead-lettered.
- Boot: runs the embedded analytical migrations over a DIRECT DSN
  (golang-migrate's advisory lock does not survive PgBouncer transaction
  pooling), opens the pool, pings. Kafka is dialled lazily. `/healthz` and
  `/readyz` on `:8091` (readiness flips to 503 as the first step of
  shutdown). A consumer that returns or an admin server that fails exits
  non-zero — a projector that consumes nothing must not keep answering its
  probes. Missing `ANALYTICS_DATABASE_URL`, `KAFKA_BROKERS` or
  `ANALYTICS_CONSUMER_GROUP` is a boot error naming all of them. It writes
  only to the analytical database, never opens the OLTP database, and calls
  no sibling context.

### 5. The reports (`cmd/returns-reports`, `:8092`)

Read-only HTTP over the analytical database through a read-only pool; `/healthz`;
RFC 7807 problems; no auth (fleet rule: the in-cluster boundary is the access
control). `cmd/api` serves no `/reports/*` route.

| endpoint | question | shape |
| --- | --- | --- |
| `GET /reports/disposition-mix` | outcome counts by policy version | `from`, `to`, `site_id?`, `rows[]`: `policy_version`, `outcome`, `lines`, `units` |
| `GET /reports/value-recovery` | restocked lines/units per site per day | `from`, `to`, `site_id?`, `granularity`, `days[]`: `day`, `sites[] {site_id, restocked_lines, restocked_units}` |
| `GET /reports/return-cycle-time` | receipt-to-staged latency percentiles | `from`, `to`, `site_id?`, `granularity`, `days[]`: `day`, `staged_lines`, `cycle_time {count, p50_seconds, p95_seconds}` |
| `GET /reports/freshness` | how far the projection is behind | `as_of` (newest applied CloudEvents time), `lag_seconds` (never negative); both `null` before the first event |

Rules:

- `from` / `to` are optional RFC 3339 instants; both omitted = the 30 days
  ending now; at most 366 days; `from` inclusive, `to` exclusive; an empty or
  inverted range is a 400 (`invalid-report-range`); any `granularity` but
  `day` is a 400 (`invalid-report-granularity`).
- `siteId` narrows every report to one site.
- A decision counts in the day its `DispositionDecided` `time` falls in; a
  staged line counts (recovery and cycle-time) in the day its `ReturnLineStaged`
  `time` falls in.
- `days[]` is dense: one row per UTC calendar day intersecting `[from, to)`,
  zeros included.
- `cycle_time` is `staged_at - opened_at` of the lines staged in the window
  whose receipt opening the projection also saw; a staged line without a seen
  opening counts in `staged_lines` but has no latency. p50/p95 are
  linear-interpolated percentiles in seconds (PostgreSQL `percentile_cont`),
  `null` when `count` is 0.
- SQL only counts, sums and takes percentiles; the range rules, day windows,
  granularity and the freshness lag live in `internal/analytics/report` (a
  self-contained region that imports no other internal package — the arch
  test guards it). The in-memory store and the Postgres store run the SAME
  contract test.

### 6. Why a separate analytical database

The projection is a derived copy that can be dropped and rebuilt from the
topic; keeping it out of the OLTP database means a heavy report never takes
an OLTP connection, the two schemas evolve independently, the reports
credentials can be a read-only role over a database that holds nothing
transactional, and the projector holds no credentials on OLTP data. The cost
is a second database to provision in `warehouse-infra` (its own DSN env,
`ANALYTICS_DATABASE_URL`).

### 7. Packaging

The one image already builds every `cmd/*` (the Dockerfile's build loop), so
`/app/returns-projector` and `/app/returns-reports` ship in it; the
analytical migrations are embedded. The chart gains two components behind
`analytics.enabled` (default `false`: the default render is unchanged):
`analytics-projector` (Deployment only — :8091 is probes, no Service) and
`analytics-reports` (Deployment + ClusterIP Service `<release>-reports:80`),
plus a Secret with `ANALYTICS_DATABASE_URL` (chart-created from
`analytics.url` or the operator's `analytics.existingSecret`). The chart
refuses to render analytics without a DSN source or a consumer group: the
binaries exit at boot otherwise (crash-loop). External routing to the
reports Service is warehouse-infra's job, not the chart's.

## Consequences

- The analytics stream has a reader; the disposition mix, value recovery,
  cycle time and freshness are queryable.
- Replaying the topic under a new consumer group rebuilds the whole model
  (the facts keep earliest-wins milestones, so order never matters).
- Events whose receipt opening predates the projection's start show up as
  staged lines without a cycle time.
- Three hexagonal isolation rules for the `internal/analytics` region are in
  `internal/architecture`: the region depends on nothing internal except
  itself, and the OLTP domain and application layers never import it or the
  analytics store.

## What is deliberately NOT reported (v1)

- **The authorization funnel** (requested vs authorized vs rejected RMAs, by
  rejection code): the twins are on the topic, so a later report can add it
  without a migration; v1 answers the disposition questions only.
- **Per-SKU / per-reason breakdowns**: the facts carry sku and the decisions
  carry supersedes/reason; a later report can group by them.
- **Recovery in currency**: units and lines only — no price data exists in
  this context (it would have to be consumed from product-master).
- **Consumed contexts' events** (order-management, fulfillment-execution):
  they are on other contexts' topics; pulling them in would couple the
  analytical database to their contracts (ADR 0003).

## Alternatives considered and rejected

- **Project from the integration topic**: the fleet convention is a dedicated
  analytics topic owned by the producer, and a sibling consumer group on the
  integration topic would double the offset bookkeeping on a contract topic.
- **Pre-aggregated daily counters**: cannot be re-bucketed for a partial
  first or last day, and a late or replayed event would need decrement
  logic. Facts are cheap at this volume.
- **Compute the reports from the OLTP tables**: the OLTP read model keeps
  current state, not the milestone history, and analytical queries would run
  on the OLTP database.
- **Hour granularity**: the operational cadence is daily; the `granularity`
  parameter exists so that can change without a new endpoint.
