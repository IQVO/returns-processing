---
id: introduction
title: Introduction
sidebar_label: Introduction
sidebar_position: 1
---

# Returns Processing

`returns-processing` is the bounded context that owns the **reverse flow**: goods coming back. It
authorizes a return against an order line that was both **allocated** (order-management) and
**shipped** (fulfillment-execution's manifest), receives the arriving parcel, records one inspection
**grade** per line, and decides the **disposition** — `RESTOCK`, `REFURBISH`, `LIQUIDATE`, `DISPOSE`
or `RTV` — from a versioned **DispositionPolicy** keyed on grade and product classification. It
never writes the stock ledger: a `RESTOCK` disposition hands the goods to inventory-storage as a
staged line (`ReturnLineStaged`), and the stow stays an RF action there.

| | |
| --- | --- |
| Tier | WMS (CloudEvents type segment `wms`, source `/warehouse/returns-processing`) |
| Subdomain class | Supporting ([reasoning](/docs/ddd/bounded-context-canvas#strategic-classification)) |
| Binaries | two: `cmd/api` (REST, outbox relay and three Kafka consumers in one process, all always on) and `cmd/mcp` (read-only MCP over Streamable HTTP, default-off chart component) |
| Datastore | PostgreSQL (required: there is no in-memory mode) |
| Messaging | Kafka, CloudEvents 1.0 structured mode, transactional outbox ([ADR 0005](/docs/adr/0005-transactional-outbox-and-relay)) |
| Auth | none (fleet decision 2026-09-11) |
| Repository | `IQVO/returns-processing`, site `https://iqvo.github.io/returns-processing/` |

## What it owns

- **ReturnAuthorization (RMA)**: permission to send one shipped order line back, in the states
  `REQUESTED → AUTHORIZED | REJECTED → RECEIVED → CLOSED`. One open RMA per order line.
- **ReturnReceipt**: the physical arrival, opened against an RMA or, for a parcel with no RMA, with
  explicitly `UNEXPECTED` lines. A line is inspected exactly once (`grade` A/B/C/SCRAP + reason
  codes).
- **Disposition**: the immutable decision per inspected line — `f(grade, classification, policy
  version)`. An operator override is a NEW disposition carrying `supersedes` and a mandatory reason.
- **DispositionPolicy**: the versioned operator table (grade × product class → outcome, return
  window days, max units per line, veto outcome). Data, not code: a change is a new version.

It does **not** own orders, shipments, classifications or stock. Those arrive as events into local
copies (`allocated_lines`, `manifested_units`, `product_classes`); the service
never calls a sibling context over REST or MCP ([ADR 0003](/docs/adr/0003-local-copies-and-consumed-contracts)).

## Main capabilities

1. **Authorize returns** with `POST /returns`, against the local copy of the manifested work unit
   `{order_id}-line-{line_no}`, inside the policy window, quantity capped by
   `max_units_per_line` — or reject with a code (`LINE_NOT_MANIFESTED`, `RETURN_WINDOW_EXPIRED`,
   `OPEN_RMA_EXISTS`, `QUANTITY_EXCEEDS_POLICY`).
2. **Receive parcels** with `POST /returns/{rmaId}/receipts`; a line with no RMA is received as
   explicitly UNEXPECTED and raises `UnexpectedReturnReceived` for exception-management.
3. **Inspect lines** with `POST /receipts/{receiptId}/lines/{lineNo}/inspect`; the grade is recorded
   and the disposition decided by the current policy in the same transaction.
4. **Override a disposition** with `POST /dispositions/{id}/override` (reason mandatory); the
   original stays immutable.
5. **Declare policy versions** with `PUT /disposition-policies/{version}` (append-only).
6. **Publish** `ReturnAuthorized`, `ReturnAuthorizationRejected`, `ReturnReceiptOpened`,
   `ReturnLineInspected`, `UnexpectedReturnReceived`, `DispositionDecided` and `ReturnLineStaged` on
   `warehouse.returns-processing.events` through the transactional outbox.
7. **Answer read-only questions over MCP**: `get_return`, `list_returns_by_order`,
   `get_disposition_policy` ([MCP tools](/docs/mcp/tools)).

## Status of the chain

Checked on each sibling's `origin/develop` (2026-10-21): the three upstream feeds are live —
product-master publishes `ProductClassified`, order-management publishes `OrderAllocated` /
`OrderPartiallyAllocated`, fulfillment-execution publishes `PackageManifested` — and both downstream
edges are built: inventory-storage consumes `ReturnLineStaged` into its staged-receipt use case (its
ADR 0044) and exception-management consumes `UnexpectedReturnReceived` to open a problem-solve case.
Two producer-side gaps are recorded as roadmap, never faked: order-management's allocated lines carry
no `quantity`, so the authorized quantity is capped by policy only; and inventory-storage's
`ItemStowed` is not on its integration topic yet, so a staged line's stow is invisible here
([ADR 0003](/docs/adr/0003-local-copies-and-consumed-contracts#planned-not-built-from-adr-0001-never-faked)).
The service is packaged (Helm chart with an api component and a default-off read-only mcp component)
but not yet registered in `warehouse-infra` and has cut no release
([Runbook](/docs/operations/runbook#deployment-status)).

## Where to go next

| I want to | Page |
| --- | --- |
| understand the layout and the moving parts | [Architecture](/docs/overview/architecture) |
| know why the flow exists, business-first | [Business context](/docs/overview/business-context) |
| run it and make the first calls | [Quickstart](/docs/overview/quickstart) |
| configure it | [Configuration](/docs/operations/configuration) |
| deploy, migrate and operate it | [Runbook](/docs/operations/runbook) |
| watch it | [Observability](/docs/operations/observability) |
| debug it | [Troubleshooting](/docs/operations/troubleshooting) |
| read the language term by term | [Ubiquitous language](/docs/ddd/ubiquitous-language) |
| read the REST and event contracts | [API reference](/docs/api-reference), [Event catalogue](/docs/api-reference/events) |
| call it from an agent | [MCP tools](/docs/mcp/tools) |
| see why it was built this way | [ADR index](/docs/adr), [Bounded context canvas](/docs/ddd/bounded-context-canvas) |
