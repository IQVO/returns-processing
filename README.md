# returns-processing

Returns Processing: WMS-tier Supporting bounded context of the warehouse-systems fleet. It authorizes returns against shipped order lines, receives and grades the goods, decides their disposition from a versioned policy and hands restockable units to `inventory-storage` through the staged-receipt seam. Hexagonal Go, Postgres, REST, Kafka (CloudEvents 1.0).

- Design and pinned contracts: `docs/adr/0001-returns-processing-bounded-context.md` and the fleet plan `.hermes/plans/2026-10-20_231017-returns-processing-and-exception-management.md`.
- Scaffolded from `warehouse-harness-template` (v4). Guides: `.claude/rules/`, sensors: `HARNESS.md`.
- Commands: `make check-fast`, `make check-all`, `make integration`.

## What it does

- `POST /returns` authorizes an RMA against an order line that was allocated (order-management events) and manifested (fulfillment-execution events), inside the policy window.
- Opens return receipts, records a grade per line and decides the disposition (RESTOCK, REFURBISH, LIQUIDATE, DISPOSE, RTV) from the current `DispositionPolicy`; overrides are new, superseding decisions with a reason.
- Publishes `ReturnLineStaged` for every RESTOCK so inventory-storage stages the stock; the stow stays an RF action there. Unmatched lines raise `UnexpectedReturnReceived` for exception-management.
- Keeps event-fed local copies of product classifications, allocated lines and manifested work units. It never calls a sibling over REST or MCP.
- Runs on real Postgres and real Kafka only (no in-memory mode). No auth.

## Web remote (console)

`web/` is the `returns_mfe` Module Federation remote for the warehouse-console
shell (ADR 0006): returns list by order, RMA detail with the receive → inspect
→ override flow, and the current disposition policy — all over the existing
REST, a fresh `Idempotency-Key` on every write POST. Dev server `:4010`
(`cd web && npm ci && npm run dev`); served in the cluster by nginx under
`/mfes/returns-processing/` (`web/Dockerfile`, chart `frontend.enabled`).
Registration in the console shell is the console repo's change.

## A2A agent

`cmd/a2a` ([ADR 0008](docs/adr/0008-a2a-inbound-adapter.md)) is a read-only Agent2Agent server: a
third driving adapter (`internal/adapters/inbound/a2a`) over the same read use cases as REST and
MCP, built on `a2aproject/a2a-go/v2`. JSON-RPC at `/` and `/a2a` on `:8093` (`A2A_ADDR`), Agent
Card at `/.well-known/agent-card.json`, `GET /healthz`; `DATABASE_URL` and the absolute
`A2A_AGENT_URL` (advertised on the card) are mandatory. No auth, no writes, no Kafka.

Two skills, called as a `SendMessage` data part `{"skill": "<id>", "arguments": {...}}`:

- `return_status` (read): exactly one of `{rmaId}` or `{orderId}` — where a return stands.
- `disposition_policy` (read): `{}` for the current policy or `{version}` for a declared one.

Rejections come back in-band as RFC 7807 `problem`s (404/400/500, REST's slugs). In Kubernetes:
Helm `a2a.enabled=true` (default `false`) renders `<release>-a2a` (ClusterIP, port 8093), exposed
through Kong at `/api/a2a/returns-processing` when `a2a.route.enabled`.

## Status

Service (domain, application, adapters), packaging (chart, Dockerfile, CI), this documentation
site and the console web remote are built on `develop` (PRs #2, #3, the docs PR and the
console-remote PR). Not yet done: first `develop` → `main`
release and `warehouse-infra` registration (`docs/before-first-release.md` is the checklist).

## Documentation

The Docusaurus site in `docs/` carries the full pack: overview (introduction, business context,
architecture, quickstart), the ddd-crew artifacts (bounded-context canvas, aggregate design
canvas, domain events, ubiquitous language), the [ADR index](docs/adr/index.md), operations
(configuration, runbook, observability, troubleshooting), the MCP tools page, the A2A ADR, and the API
reference. It publishes to GitHub Pages at `https://iqvo.github.io/returns-processing/` on every
push to `main` (`docs.yml`); pull requests build it as a check.

- Build locally: `cd docs && npm ci && npm run build` (static site in `docs/build`).
- The REST reference pages under `docs/docs/api-reference/rest/` are GENERATED from
  `apis/openapi.yaml` (`npm run gen-api-docs` in `docs/`). Never edit them by hand: the
  `docs-api-drift` CI job regenerates them and fails any PR where they differ from the spec.
- ADRs live in `docs/adr/` and are served in place by the site; bodies are immutable — a changed
  decision gets a new ADR.
