---
id: use-cases
title: Use cases
sidebar_label: Use cases
sidebar_position: 1
description: Every application use case — the three copy-applying consumers, the five REST commands and the reads — with the cross-aggregate rule each one enforces.
---

# Use cases

The application layer (`internal/application/usecases`) is thin: each use
case enforces one cross-aggregate rule, loads what it needs through the
ports, saves inside one unit of work and hands the domain events to the
outbox. This is the complete list — there is no use case that rejects an
RMA (a refusal persists nothing), none that re-inspects a line, and none
that mutates a disposition.

## Consumer-driven use cases

### 1. `ApplyProductClassification` (`consume_classifications.go`)

One consumed `ProductClassified` upserts the `product_classes` copy: one
row per SKU (tags, temperature class, the source version), applied only
when the event's `version` is greater than the stored one. The CloudEvents
`id` is claimed in the same transaction; a replayed id is a no-op that
still commits the offset. A SKU with no classification yet is treated as
unclassified at decision time — fail-open for grading, fail-closed for a
hazmat RESTOCK once known (ADR 0001/0003).

### 2. `ApplyAllocations` (`consume_allocations.go`)

One consumed `OrderAllocated` or `OrderPartiallyAllocated` upserts the
`allocated_lines` copy: one row per (order_id, line_no) with the
authoritative SKU. The sources carry no version, so a newer fact is
last-write-wins and a partial allocation refreshes only its own lines.
This copy is why `POST /returns` takes no SKU — the copy is the truth.

### 3. `ApplyManifest` (`consume_manifests.go`)

One consumed `PackageManifested` inserts into `manifested_units`: the
`order_ref` is reversed with the work-unit formula
(`shared.OrderLineRef.WorkUnit()`, `{order_id}-line-{line_no}`) and the row
is idempotent on the work-unit key, with the package id as a second unique
key. An empty copy is the `LINE_NOT_MANIFESTED` rejection at authorization
time — fail-closed, never a live call.

## REST command use cases

### 4. `AuthorizeReturn` (`authorize_return.go`)

`POST /returns` with `{orderId, lineNo, quantity, siteId, reasonCode}`:
composes the invariants of ADR 0002 into one decision — the allocations
copy supplies the SKU, the manifested copy proves the line shipped, the
one-open-RMA query and the current policy (window, cap) complete the facts —
and the pure domain decides in one transaction. Authorized: the RMA at
version 1, `ReturnAuthorized` in the same unit of work. Refused: the typed
domain error (`ErrLineNotManifested`, `ErrWindowExpired`,
`ErrOpenRMAExists`, `ErrQuantityExceedsPolicy`) mapped to its rejection
code at the edge; nothing is persisted and nothing is published — the
partial unique index `uq_rmas_one_open_per_order_line` backs the one-open
rule against races. A line the allocations copy does not know is a 404
(`ErrOrderLineNotAllocated`).

### 5. `OpenReturnReceipt` (`open_return_receipt.go`)

`POST /returns/{rmaId}/receipts` with `{siteId, lines[]}`: records the
physical arrival. Each line is built as MATCHED (it carries an RMA id) or
UNEXPECTED (it carries none) — the constructor refuses the third state.
Raises `ReturnReceiptOpened` (all lines) plus one
`UnexpectedReturnReceived` per unexpected line, which exception-management
opens a case on. The receipt id is service-minted (`rcpt-` prefix, per the
OpenAPI pattern). v1 does not transition the RMA on receipt; the receipt
carries the arrival fact.

### 6. `InspectReturnLine` (`inspect_return_line.go`)

`POST /receipts/{receiptId}/lines/{lineNo}/inspections` with `{grade, quantity,
reasonCodes}`: grades the line (exactly once, inspected qty in
[1, received qty]) and decides its disposition in the same unit of work:
the current policy, the classification from the product copy and the grade
go into the pure `Decide`; the resulting `DispositionDecided` — and
`ReturnLineStaged` when the outcome is RESTOCK — publishes with the save.
A wrong grade is corrected by an override, never by re-inspecting.

### 7. `OverrideDisposition` (`override_disposition.go`)

`POST /dispositions/{dispositionId}/overrides` with `{outcome, reason}`:
creates a NEW immutable Disposition carrying `supersedes` and the mandatory
reason, keeping the original's line, grade, quantity and `policy_version`
(the version under discussion); the original is never mutated. A RESTOCK
override stages the line like any restock — `ReturnLineStaged` exactly
once, idempotent downstream on the new disposition id.

### 8. `DeclareDispositionPolicy` (`declare_disposition_policy.go`)

`PUT /disposition-policies/{version}` with `{returnWindowDays,
maxUnitsPerLine, vetoOutcome?, rules[]}`: declares a policy version and
marks it current (`Save` clears the previous current in the same
transaction). Validation refuses a policy without a `*` default per grade —
`Decide` must stay total — and a duplicate (grade, productClass) rule. The
domain holds no declaration fact: the policy is data, read by `Decide`,
never evented (ADR 0002).

## Read use cases

### 9. `GetReturn` (`reads.go`)

`GET /returns/{rmaId}` — the RMA: order line, SKU, quantity, site, reason,
status, rejection code, window, decision instant. Also behind the
`get_return` MCP tool.

### 10. `ListReturnsByOrder`

`GET /returns?orderRef=…` — the RMAs of one order by line then id, one keyset page at a time (`limit`, `cursor`, `{items,nextCursor}`); `[]` for
an unknown order, never a 404. Also behind the `list_returns_by_order` MCP
tool.

### 11. `GetDispositionPolicy`

`GET /disposition-policies/current` — the current policy version: window,
cap, veto outcome and the full rule table. Also behind the
`get_disposition_policy` MCP tool.

## The reports read side (ADR 0007)

`cmd/returns-reports` (`:8092`, read-only pool over the separate analytics
database) serves `GET /reports/disposition-mix`, `/reports/value-recovery`,
`/reports/return-cycle-time` and `/reports/freshness`, each with optional
`from`/`to`/`siteId` and `granularity=day`. These are projections counted
from fact rows, not use cases over the aggregates; see
[Analytics](/docs/operations/analytics).

## Read models

OLTP reads project the aggregate tables directly (the row IS the snapshot;
there are no separate OLTP read-model tables), and the three local copies
are the read models of other contexts' facts (ADR 0003).
`internal/application/views` holds the wire DTOs; no aggregate type leaks
past the application layer. The analytics database is a real read model —
facts, not aggregates — rebuilt by replaying the topic.
