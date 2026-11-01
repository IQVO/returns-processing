# returns-processing

[![CI](https://github.com/IQVO/returns-processing/actions/workflows/ci.yml/badge.svg?branch=develop)](https://github.com/IQVO/returns-processing/actions/workflows/ci.yml?query=branch%3Adevelop) [![coverage gate](https://img.shields.io/badge/coverage%20gate-90%25-brightgreen)](https://github.com/IQVO/returns-processing/blob/develop/Makefile) [![Go](https://img.shields.io/badge/go-1.27.3-00ADD8?logo=go&logoColor=white)](https://github.com/IQVO/returns-processing/blob/develop/go.mod) [![docs](https://img.shields.io/badge/docs-iqvo.github.io-blue)](https://iqvo.github.io/returns-processing/) [![license](https://img.shields.io/badge/license-MIT-blue)](https://github.com/IQVO/returns-processing/blob/develop/LICENSE)

The **Returns Processing** bounded context of the `warehouse-systems` fleet — a
WMS-tier **Supporting Subdomain**. It authorizes returns against shipped order
lines, receives and grades the returned goods, decides each line's disposition
from a versioned policy, and hands restockable units to `inventory-storage`
through the staged-receipt seam.

It owns the `ReturnAuthorization`, `ReturnReceipt` and `Disposition` aggregates
plus the versioned `DispositionPolicy`. It does **not** own stock (that stays
in `inventory-storage`; the stow remains an RF action there), not the order
(`order-management`), not the exception case (`exception-management` owns the
case opened from this context's `UnexpectedReturnReceived`). It is an **Open
Host Service** with a **Published Language**: its REST API and its domain
events on `warehouse.returns-processing.events`. It keeps event-fed local
copies of product classifications, allocated order lines and manifested work
units, and never calls a sibling over REST or MCP. Downstream:
`inventory-storage` consumes `ReturnLineStaged`; `exception-management`
consumes `UnexpectedReturnReceived`. Hexagonal Go, Postgres, REST, Kafka
(CloudEvents 1.0).

## Disclaimer

This project is a domain-driven design study. It is not associated with,
endorsed by or affiliated with any company, and it uses no company's code,
data, logos or trademarks. All names, figures and examples are illustrative
or synthetic.

---

## Documentation

The Docusaurus site in `docs/` carries the full pack: overview (introduction,
business context, architecture, quickstart), the ddd-crew artifacts
(bounded-context canvas, aggregate design canvas, domain events, ubiquitous
language), the [ADR index](docs/adr/index.md), operations (configuration,
runbook, observability, troubleshooting, analytics), the MCP tools page, the
A2A ADR, and the API reference. It publishes to GitHub Pages at
**<https://iqvo.github.io/returns-processing/>** on every push to `main`
(`docs.yml`); pull requests build it as a check.

Page map (source under `docs/docs/`):

| Area | Pages |
|---|---|
| Overview | [Introduction](docs/docs/overview/introduction.md), [Business context](docs/docs/overview/business-context.md), [Architecture](docs/docs/overview/architecture.md), [Quickstart](docs/docs/overview/quickstart.md) |
| Operations | [Configuration](docs/docs/operations/configuration.md), [Runbook](docs/docs/operations/runbook.md), [Observability](docs/docs/operations/observability.md), [Troubleshooting](docs/docs/operations/troubleshooting.md), [Analytics](docs/docs/operations/analytics.md) |
| Development | [Testing](docs/docs/development/testing.md) |
| MCP | [MCP tools](docs/docs/mcp/tools.md) |
| DDD | [Use cases](docs/docs/ddd/use-cases.md), [Ubiquitous language](docs/docs/ddd/ubiquitous-language.md), [Domain events](docs/docs/ddd/domain-events.md), [Bounded context canvas](docs/docs/ddd/bounded-context-canvas.md), [Aggregate design canvas](docs/docs/ddd/aggregate-design-canvas.md), [Context map](docs/docs/ddd/context-map.md), [Core domain chart](docs/docs/ddd/core-domain-chart.md), [Domain message flow](docs/docs/ddd/domain-message-flow.md), [EventStorming](docs/docs/ddd/eventstorming.md), [Class diagram](docs/docs/ddd/class-diagram.md), [Sequence diagrams](docs/docs/ddd/sequence-diagrams.md), [Entity-relationship](docs/docs/ddd/entity-relationship.md) |
| Reference | [API overview](docs/docs/api-reference/overview.md), [Event catalogue](docs/docs/api-reference/events.md), generated REST pages under `docs/docs/api-reference/rest/` |
| Decisions | [ADR index](docs/adr/index.md) |

- Build locally: `cd docs && npm ci && npm run build` (static site in `docs/build`).
- The REST reference pages under `docs/docs/api-reference/rest/` are GENERATED
  from `apis/openapi.yaml` (`npm run gen-api-docs` in `docs/`). Never edit
  them by hand: the `docs-api-drift` CI job regenerates them and fails any PR
  where they differ from the spec.
- ADRs live in `docs/adr/` and are served in place by the site; bodies are
  immutable — a changed decision gets a new ADR.
- Design and pinned contracts:
  [ADR 0001](docs/adr/0001-returns-processing-bounded-context.md).
- Scaffolded from `warehouse-harness-template` (v4). Guides: `.claude/rules/`,
  sensors: `HARNESS.md`.

---

## What it does

- `POST /returns` authorizes an RMA against an order line that was allocated
  (order-management events) and manifested (fulfillment-execution events),
  inside the policy window.
- Opens return receipts, records a grade per line and decides the disposition
  (RESTOCK, REFURBISH, LIQUIDATE, DISPOSE, RTV) from the current
  `DispositionPolicy`; overrides are new, superseding decisions with a reason.
- Publishes `ReturnLineStaged` for every RESTOCK so inventory-storage stages
  the stock; the stow stays an RF action there. Unmatched lines raise
  `UnexpectedReturnReceived` for exception-management.
- Keeps event-fed local copies of product classifications, allocated lines and
  manifested work units (ADR 0003). It never calls a sibling over REST or
  MCP.
- Runs on real Postgres and real Kafka only (no in-memory mode). No auth.

## Status

Service (domain, application, adapters), packaging (chart, Dockerfile, CI),
the documentation site and the console web remote are built on `develop`. Not
yet done: first `develop` → `main` release and `warehouse-infra` registration
(`docs/before-first-release.md` is the checklist).

---

## Architecture

Hexagonal / ports & adapters; dependencies point inward only:

```
cmd/api/                      composition root: REST service, outbox relay, the three local-copy consumers
cmd/mcp/                      read-only MCP server (Streamable HTTP)
cmd/a2a/                      read-only A2A agent (JSON-RPC over HTTP, Agent Card at /.well-known/agent-card.json)
cmd/returns-projector/        analytics projector (the only analytical-DB writer)
cmd/returns-reports/          analytics reader (serves the /reports routes)
internal/
  domain/                     pure business logic
    returnauthorization/      ReturnAuthorization aggregate
    returnreceipt/            ReturnReceipt aggregate (receipt lines, grading)
    disposition/              Disposition decisions + versioned DispositionPolicy
    shared/                   ids, the event header, domain events
  application/
    ports/                    OUT interfaces only (repos, EventPublisher, UnitOfWork, Clock)
    usecases/                 one struct per use case, incl. the read-model assemblers
    views/                    read models for REST/MCP/A2A
    outbox/                   transactional outbox (ADR 0005)
  adapters/
    inbound/http/             chi handlers, DTOs, RFC 7807 error mapping, Idempotency-Key middleware
    inbound/mcp/              the three read-only MCP tools
    inbound/a2a/              A2A skills: return_status, disposition_policy (both reads)
    inbound/kafka/            the three local-copy consumers + the projector's analytics consumer
    kafka/cloudevents/        the CloudEvents 1.0 envelope
    outbound/postgres/        pgxpool repos, golang-migrate runner, UnitOfWork, outbox + relay
    outbound/kafka/           integration + analytics topic publishers
    outbound/analyticsstore/  analytical DB projection and report queries
    outbound/memory/          in-memory test doubles (never linked into a binary)
  analytics/report/           the reports data product model
migrations/                   golang-migrate SQL (OLTP); analytics/migrations/ (analytical DB)
```

The rule is enforced as an executable test, not a convention:
`internal/architecture/architecture_test.go` (arch-go) fails the build on any
layering violation, and its sibling fleet fitness tests keep the invariants
that matter here: `runtime_no_memory_fitness_test.go` (no in-memory adapter
in a runtime binary), `kafka_writer_topic_fitness_test.go`,
`kafka_writer_acks_fitness_test.go`, `kafka_dlq_shutdown_fitness_test.go`,
`outbox_relay_cap_fitness_test.go`, `events_fitness_test.go`,
`catalogue_fitness_test.go`, `mcp_optional_inputs_test.go`,
`a2a_fitness_test.go` and `no_go_files_at_root_fitness_test.go`.

---

## Domain events

All seven published events — `ReturnAuthorized`,
`ReturnAuthorizationRejected`, `ReturnReceiptOpened`, `ReturnLineInspected`,
`UnexpectedReturnReceived`, `DispositionDecided`, `ReturnLineStaged` — go
through the transactional outbox (ADR 0005) to BOTH
`warehouse.returns-processing.events` (the integration topic other contexts
read) and `warehouse.returns-processing.analytics` (this service's own
analytics stream, ADR 0007), specified in `apis/asyncapi.yaml`.

Every Kafka message on both topics is a **CloudEvents 1.0** event in
structured content mode (ADR 0004), header
`content-type: application/cloudevents+json; charset=UTF-8`, `subject` = the
aggregate id, `dataschema` =
`urn:warehouse:returns-processing:<events|analytics>:<EventName>:v1`.
CloudEvents `type` convention, identical to the other warehouse-systems
services — this context's subdomain segment is `wms`:

```
com.warehouse.wms.returns-processing.<entity>.<EventName>

com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorized
com.warehouse.wms.returns-processing.returnreceipt.UnexpectedReturnReceived
com.warehouse.wms.returns-processing.disposition.ReturnLineStaged
```

Consumed (local copies, ADR 0003; one pinned consumer group each):

| Topic | Types consumed | Consumer group env |
|---|---|---|
| `warehouse.product-master.events` | `ProductClassified` | `PRODUCT_CLASSIFICATION_CONSUMER_GROUP` |
| `warehouse.order-management.events` | `OrderAllocated`, `OrderPartiallyAllocated` | `ORDER_LINES_CONSUMER_GROUP` |
| `warehouse.fulfillment.events` | `PackageManifested` | `MANIFEST_CONSUMER_GROUP` |

The projector consumes `warehouse.returns-processing.analytics` under
`ANALYTICS_CONSUMER_GROUP` and dead-letters poison rows to
`warehouse.returns-processing.analytics.dlq`; other contexts keep reading
`warehouse.returns-processing.events`.

---

## Running it

Postgres and Kafka are the only runtime: there is no in-memory mode and no
log-only publisher. `cmd/api` exits at boot with an error naming every
missing variable.

```sh
docker compose up -d --wait   # postgres:16-alpine on localhost:5433 (returns/returns/returns)
                              # NOTE: the compose file starts NO Kafka — point
                              # KAFKA_BROKERS at a fleet broker (the cluster maps :9092)
export DATABASE_URL="postgres://returns:returns@localhost:5433/returns?sslmode=disable"
export KAFKA_BROKERS="localhost:9092"
export PRODUCT_CLASSIFICATION_CONSUMER_GROUP=returns-local-products
export ORDER_LINES_CONSUMER_GROUP=returns-local-order-lines
export MANIFEST_CONSUMER_GROUP=returns-local-manifests
go run ./cmd/api              # migrations run automatically at startup; listens on :8080
```

### Configuration

`cmd/api`:

| Env var | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | **required** | Postgres DSN; unset ⇒ boot error naming it |
| `KAFKA_BROKERS` | **required** | comma-separated broker list |
| `PRODUCT_CLASSIFICATION_CONSUMER_GROUP` | **required** | pinned group id on `warehouse.product-master.events` |
| `ORDER_LINES_CONSUMER_GROUP` | **required** | pinned group id on `warehouse.order-management.events` |
| `MANIFEST_CONSUMER_GROUP` | **required** | pinned group id on `warehouse.fulfillment.events` |
| `HTTP_ADDR` | `:8080` | REST listen address |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Go duration (or plain seconds) between relay passes |
| `OUTBOX_MAX_ATTEMPTS` | `20` | attempts before the relay excludes a row (`0` = unlimited) |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | wait after flipping `/readyz` before closing the listener (`0` = none) |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | direct (non-PgBouncer) DSN for the boot migrations |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` (`warning` accepted), case-insensitive |

`cmd/mcp`: `MCP_ADDR` (`:8090`), `DATABASE_URL` (**required**),
`MIGRATIONS_DATABASE_URL`, `LOG_LEVEL`.
`cmd/a2a`: `A2A_ADDR` (`:8093`), `DATABASE_URL` (**required**),
`A2A_AGENT_URL` (**required**, the absolute URL advertised on the Agent
Card), `MIGRATIONS_DATABASE_URL`, `LOG_LEVEL`.
`cmd/returns-projector`: `ANALYTICS_DATABASE_URL` (**required**),
`KAFKA_BROKERS` (**required**), `ANALYTICS_CONSUMER_GROUP` (**required**),
`ADMIN_ADDR` (`:8091`), `ANALYTICS_MIGRATIONS_PATH`
(`analytics/migrations`), `LOG_LEVEL`.
`cmd/returns-reports`: `ANALYTICS_DATABASE_URL` (**required**),
`HTTP_ADDR` (`:8092`), `LOG_LEVEL`.

### Analytics data product (returns analytics)

An additive analytical read side (ADR 0007) built from this service's own
domain events, delivered on the separate topic
`warehouse.returns-processing.analytics`, projected into a **separate**
analytical database, and served read-only. Three processes with one writer:

```sh
# 1. OLTP service — fans every domain event out to BOTH topics (see above)
go run ./cmd/api

# 2. Projector — the ONLY writer of the analytical DB (runs analytics/migrations itself)
export ANALYTICS_DATABASE_URL="postgres://returns:***@localhost:5433/returns_analytics?sslmode=disable"
KAFKA_BROKERS=localhost:9092 ANALYTICS_CONSUMER_GROUP=returns-analytics go run ./cmd/returns-projector   # :8091

# 3. Reports — read-only reader over a read-only pool
ANALYTICS_DATABASE_URL="$ANALYTICS_DATABASE_URL" go run ./cmd/returns-reports   # :8092
curl 'http://localhost:8092/reports/disposition-mix?from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:00Z'
curl http://localhost:8092/reports/return-cycle-time http://localhost:8092/reports/value-recovery
curl http://localhost:8092/reports/freshness
```

---

## Observability

What exists today, and what deliberately does not (see the
[observability page](docs/docs/operations/observability.md)):

- **Logs** — one JSON line per event (`log/slog`, stdout), level from
  `LOG_LEVEL`. Boot problems are one aggregated `invalid configuration: ...`
  error naming every missing variable; readiness flips
  (`not ready: postgres` / `not ready: kafka`), outbox relay dead-letter and
  attempt-cap lines, and consumer skip/dead-letter lines are the ones an
  operator greps for.
- **Probes** — `GET /healthz` (liveness only) and `GET /readyz` (the drain
  gate AND a Postgres ping AND a Kafka broker metadata check, cached a few
  seconds).
- **Metrics — none.** The service exports no runtime metrics: no Prometheus
  registry, no OTel meters, and `apis/openapi.yaml` declares no `/metrics`
  route (it was dropped in the REST guideline cutover so the contract matches
  the binary). The only runtime signals today are the logs and the two probes.
- **Traces — no SDK wired.** Outgoing Kafka messages MAY carry W3C
  `traceparent`/`tracestate` when the writing request provided them; the
  service starts no spans of its own (the A2A adapter's `otel.Tracer` calls
  are no-ops without a provider).

---

## API

Errors are **RFC 7807** (`application/problem+json`) throughout. Every route
below is declared at the same path in `apis/openapi.yaml`.

| Method | Path | Purpose | Success |
|---|---|---|---|
| `POST` | `/returns` | AuthorizeReturn (against an allocated + manifested line) | `201` + `Location` |
| `GET` | `/returns?orderRef=&limit=&cursor=` | List the RMAs of an order, one keyset page at a time (`{items,nextCursor}`) | `200` |
| `GET` | `/returns/{rmaId}` | Get one return (RMA detail) | `200` |
| `POST` | `/returns/{rmaId}/receipts` | OpenReturnReceipt | `201` + `Location` |
| `POST` | `/receipts/{receiptId}/lines/{lineNo}/inspections` | InspectReturnLine (grade + disposition) | `200` |
| `POST` | `/dispositions/{dispositionId}/overrides` | OverrideDisposition (superseding decision, with reason) | `200` |
| `PUT` | `/disposition-policies/{version}` | DeclareDispositionPolicy (versioned) | `201` |
| `GET` | `/disposition-policies/current` | Get the current policy | `200` |
| `GET` | `/healthz` / `/readyz` | Liveness / readiness (drain gate + Postgres + Kafka) | `200` |
| `GET` | `/reports/disposition-mix`, `/reports/value-recovery`, `/reports/return-cycle-time`, `/reports/freshness` | Analytics reports, served by the separate `returns-reports` binary (`:8092`); bounded by `from`/`to`, so unpaginated (`x-pagination-exempt`) | `200` |

### Idempotency

Every write route — the four `POST`s and
`PUT /disposition-policies/{version}` — requires a caller-supplied
`Idempotency-Key` header (Postgres-backed idempotency pool, migration
`0002_idempotency_keys`). A retry with the same key, method, path and body
replays the original response verbatim with `Idempotency-Replayed: true`; the
same key with a different method, path or body is rejected `409`
(`idempotency-key-reused`); a missing header is `400`
(`idempotency-key-required`), a malformed one `400` (`idempotency-key-invalid`).

`apis/openapi.yaml` (OpenAPI 3.0.3) documents every route with full schemas
and a shared `Problem` component; `apis/asyncapi.yaml` (AsyncAPI 2.6)
specifies both topics and all messages. The OpenAPI document is linted in CI
(`api-lint`, Spectral).

---

## Helm chart

```sh
helm install returns-processing ./charts/returns-processing \
  --set database.url="postgres://returns:***@postgres:5432/returns?sslmode=disable" \
  --set kafka.brokers="kafka:9092"
```

The chart refuses to render without `database.url` (or
`database.existingSecret`), `kafka.brokers` and the three pinned consumer
groups (`config.*ConsumerGroup`). Optional components, all off by default:
`mcp`, `analytics` (projector + reports), `frontend` and `a2a`. Chart
invariants are asserted by `charts/returns-processing/tests/`
(`render_test.sh`, `test_service_selectors.py`, `test_kong_config.py`) and
the `chart-tests` CI job; `helm-lint` runs `ct lint --charts
charts/returns-processing` in CI.

### Running the MCP server in Kubernetes

`cmd/mcp` ships in the same image as `/app/mcp` and is a separate, opt-in
deployable: `mcp.enabled=true` renders a `<release>-mcp` Deployment and
ClusterIP Service on port `8090`. It reads the same `DATABASE_URL` secret,
runs the OLTP migrations on start (idempotent), and serves MCP over
Streamable HTTP at `/` and `/mcp` with `GET /healthz` behind the probes. Its
surface is **read-only**: three tools — `get_return`,
`list_returns_by_order`, `get_disposition_policy` — over the same read use
cases as REST. There is no authentication layer in front of it; access
control is the in-cluster ClusterIP boundary.

### Running the A2A agent in Kubernetes

`cmd/a2a` ([ADR 0008](docs/adr/0008-a2a-inbound-adapter.md)) is the third
driving adapter (`internal/adapters/inbound/a2a`) over the same read use
cases as REST and MCP, built on `a2aproject/a2a-go/v2`: JSON-RPC at `/` and
`/a2a` on `:8093` (`A2A_ADDR`), the Agent Card at
`/.well-known/agent-card.json`, `GET /healthz`. `DATABASE_URL` and the
absolute `A2A_AGENT_URL` are mandatory; no Kafka, no writes. Two skills,
called as a `SendMessage` data part `{"skill": "<id>", "arguments": {...}}`:

- `return_status` (read): exactly one of `{rmaId}` or `{orderId}` — where a
  return stands.
- `disposition_policy` (read): `{}` for the current policy or `{version}`
  for a declared one.

Rejections come back in-band as RFC 7807 `problem`s (404/400/500, REST's
slugs). `a2a.enabled=true` (default `false`) renders `<release>-a2a`
(ClusterIP, port 8093), exposed through Kong at
`/api/a2a/returns-processing` when `a2a.route.enabled`.

---

## Operator micro-frontend (`web/`)

`web/` is the `returns_mfe` Module Federation remote for the
`warehouse-console` shell (ADR 0006): returns list by order, RMA detail with
the receive → inspect → override flow, and the current disposition policy —
all over the existing REST. Standalone development:

```sh
cd web && npm ci && npm run dev   # http://localhost:4010 (talks to :8080)
```

Deployed to the kind cluster it is built into a static bundle and served by
its own `nginx-unprivileged` pod under `/mfes/returns-processing/`
(`web/Dockerfile`, chart `frontend.enabled`). The remote reads its API
origin at runtime from `window.__WAREHOUSE_CONFIG__.apiOrigin`
(`${apiOrigin}/api/returns-processing`, published by the console shell) —
`web/src/config.ts`. Registration in the console shell is the console
repo's change.

---

## Local development / quality gate

Every CI sensor is also a `make` target (`make help` lists them all):

```sh
make check-fast   # fmt-check, vet, arch-test
make check        # fmt-check, vet, build, lint, test
make check-all    # check + coverage (90% gate) + arch-test + bdd
make integration  # testcontainers Postgres + Kafka (needs only Docker)
make mutation     # gremlins over ./internal/domain
make vuln         # govulncheck ./...
make api-lint     # spectral over apis/openapi.yaml
make guide-lint   # agent guides: references resolve, context budget
```

Git hooks are managed by [lefthook](https://github.com/evilmartians/lefthook)
(`lefthook.yml`): pre-commit runs `fmt-check` + `vet` + `lint`; pre-push runs
`make check`. Activate once per clone: `brew install lefthook && lefthook
install`.

## Tests

```sh
go test ./... -race                                  # unit
go test -tags=integration ./... -race -count=1       # testcontainers Postgres/Kafka
go test ./features/ -run TestFeatures -v             # godog/Gherkin BDD over the real HTTP API
go test ./internal/architecture/... -v               # arch-go + fleet fitness tests
make coverage                                        # domain+application coverage, CI gate >= 90%
```

CI (`.github/workflows/ci.yml`) runs `lint`, `guide-lint`, `complexity`,
`test` (with the 90% coverage gate), `bdd`, `integration`, `mutation-fast`,
`drift` and `mutation` (scheduled), `api-lint`, `vuln`, `docs-api-drift`,
`helm-lint`, `chart-tests`, `arch-test`, `web` on every push/PR to
`develop`/`main`; `trivy-scan` only on PRs into `main`; `docker-publish` and
`release` on push to `main`.

## Architecture Decision Records

The ADR index lives at [docs/adr/index.md](docs/adr/index.md) — 8 decisions,
from the bounded context itself to the CloudEvents catalogue, the
transactional outbox, the console remote, the analytics read side and the
A2A inbound adapter.

## License

MIT — see [LICENSE](LICENSE).
