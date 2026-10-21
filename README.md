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

## Status

Service (domain, application, adapters), packaging (chart, Dockerfile, CI) and this documentation
site are built on `develop` (PRs #2, #3 and the docs PR). Not yet done: first `develop` → `main`
release and `warehouse-infra` registration (`docs/before-first-release.md` is the checklist).

## Documentation

The Docusaurus site in `docs/` carries the full pack: overview (introduction, business context,
architecture, quickstart), the ddd-crew artifacts (bounded-context canvas, aggregate design
canvas, domain events, ubiquitous language), the [ADR index](docs/adr/index.md), operations
(configuration, runbook, observability, troubleshooting), the MCP tools page, and the API
reference. It publishes to GitHub Pages at `https://iqvo.github.io/returns-processing/` on every
push to `main` (`docs.yml`); pull requests build it as a check.

- Build locally: `cd docs && npm ci && npm run build` (static site in `docs/build`).
- The REST reference pages under `docs/docs/api-reference/rest/` are GENERATED from
  `apis/openapi.yaml` (`npm run gen-api-docs` in `docs/`). Never edit them by hand: the
  `docs-api-drift` CI job regenerates them and fails any PR where they differ from the spec.
- ADRs live in `docs/adr/` and are served in place by the site; bodies are immutable — a changed
  decision gets a new ADR.
