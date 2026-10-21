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

Bootstrap commit: harness, guides and ADR 0001. Contracts, domain, application, adapters, packaging and docs follow the plan's waves.
