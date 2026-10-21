# ADR 0001: returns-processing as the owner of the reverse flow (RMA, return receipt, disposition)

## Status

Accepted (2026-10-20).

## Context

No context in the fleet owns goods coming back. `inbound-receiving` owns ASNs,
dock appointments and receipts for supplier inbound. `inventory-storage` owns
the stock ledger and only learns about new stock through a staged receipt line
(`ReceiptLineReceived`) followed by an RF stow. `order-management` owns the
order and its promise; `fulfillment-execution` ships packages
(`PackageManifested`). A returned parcel today has no authorization, no
receipt, no grading and no decision about what happens to it: either it is
hand-keyed as a supplier receipt (wrong provenance, no audit) or it is lost.

Returns are a decision-driven flow, not a receiving variant: the decision
(restock, refurbish, liquidate, dispose, return to vendor) depends on an
inspection grade, the product's classification and a policy, and only ONE of
those outcomes touches the ledger. That lifecycle and policy object belong to a
context of their own.

## Decision

Introduce `returns-processing` as a new bounded context. It owns the
**ReturnAuthorization** (RMA), the **ReturnReceipt** (physical arrival and
inspection) and the **Disposition** (the policy outcome per inspected line). It
never writes the stock ledger: a `RESTOCK` disposition hands the goods to
`inventory-storage` by event (`ReturnLineStaged`, same shape as
`ReceiptLineReceived`), and the stow stays an RF action in that context.

### Bounded context canvas

| Field | Value |
|---|---|
| Name | returns-processing |
| Purpose | Authorize, receive, grade and disposition returned goods; put restockable units back into the ledger through the existing staged-receipt seam; keep every other outcome auditable. |
| Strategic classification | **Supporting subdomain**. Value recovery is material (returns run 20-30 % of e-commerce volume) but the differentiator is the ledger it feeds; the disposition policy is a replaceable policy object. |
| Tier / CloudEvents subdomain | **WMS**, subdomain `wms` (receipts and stock provenance are WMS work, the precedent is `inbound-receiving`). Type prefix `com.warehouse.wms.returns-processing.`, source `/warehouse/returns-processing`. Entity segments: `returnauthorization`, `returnreceipt`, `disposition`. |
| Domain roles | System of record for RMA, return receipt and disposition. Open Host Service for return facts (REST + events). Conformist to `product-master` (classification) and `order-management` / `fulfillment-execution` (what was ordered and shipped). |
| Inbound communication | Events: `ProductClassified` (product-master), `OrderAllocated` / `OrderPartiallyAllocated` (order-management: order id, line no, sku), `PackageManifested` (fulfillment-execution: the shipped fact per work unit). REST commands from operators and the console. No live REST/MCP call to any sibling, ever. |
| Outbound communication | `ReturnAuthorized`, `ReturnAuthorizationRejected`, `ReturnReceiptOpened`, `ReturnLineInspected`, `DispositionDecided`, `ReturnLineStaged`, `UnexpectedReturnReceived` on `warehouse.returns-processing.events` (ADR 0004 catalogue), analytics copies on `warehouse.returns-processing.analytics`; REST reads for operators, the console and agents. |
| Ubiquitous language | RMA (return authorization), return receipt, return line, grade (A, B, C, SCRAP), reason code, disposition (RESTOCK, REFURBISH, LIQUIDATE, DISPOSE, RTV), disposition policy, return window, unexpected return, staged line. |
| Business decisions | A return is authorizable only against a shipped line (manifested work unit `{order_id}-line-{line_no}`) inside the policy window; one open RMA per order line; every receipt line references an RMA line or is explicitly UNEXPECTED; a line is inspected once; the disposition is a pure function of (grade, classification, policy version) and is immutable (an override creates a superseding disposition with a reason); `ReturnLineStaged` is raised once per RESTOCK disposition. |
| Assumptions | The quantity shipped per line is NOT on `OrderAllocated` today (lines carry `line_no`, `sku`, `path_id`, no quantity): v1 caps the authorized quantity by `DispositionPolicy.maxUnitsPerLine` and records the shipped-quantity check as `planned / not built` until order-management adds `quantity` to its released line (additive producer change, asked separately). Hazmat / temperature classes come from `ProductClassified`; a SKU with no classification yet is treated as unclassified (fail-open for grading, fail-closed for RESTOCK of a hazmat SKU). |
| Verification | Mutation gate over every domain package; golden exact-JSON test per published type; godog scenarios for the RMA -> receipt -> inspect -> disposition -> staged path and for the unexpected-return path; an end-to-end test on real Postgres + Kafka (testcontainers) that drives the service only through Kafka and REST. |

### Context map

| Edge | Pattern | Direction |
|---|---|---|
| product-master -> returns-processing | Conformist (local copy of `ProductClassified`) | upstream -> us |
| order-management -> returns-processing | Conformist (local copy of allocated lines) | upstream -> us |
| fulfillment-execution -> returns-processing | Conformist (local copy of manifested work units) | upstream -> us |
| returns-processing -> inventory-storage | Open Host Service / Published Language: `ReturnLineStaged` consumed into the staged-receipt use case (inventory-storage ADR, its own PR) | us -> downstream |
| returns-processing -> exception-management | Published Language: `UnexpectedReturnReceived` opens a problem-solve case | us -> downstream |
| returns-processing -> warehouse-console, warehouse-ops-agent | REST / read-only MCP | us -> readers |

### Explicitly excluded (v1)

- Carrier label / pickup booking (external party; would need an ACL with a
  `*_MODE=stub` like network-fulfillment). Separate decision.
- Refund or credit issuing (finance, not warehouse).
- Refurbishment work orders (WES task; if ever, a `fulfillment-execution`
  task type, not this context).
- Writing `StockUnit` or `Bin` (the ledger stays in inventory-storage).
- Any live REST or MCP lookup of a sibling context at request time.

## Consequences

- Returned goods get provenance, a grade and an auditable decision; restock
  reuses the proven staged-receipt -> stow path, so inventory-storage's
  "every item has exactly one bin or is Unlocated" rule is untouched.
- Two producer-side follow-ups are recorded as roadmap, never faked:
  order-management `quantity` on released lines; inventory-storage
  `ItemStowed` on its integration topic (closes the restock loop).
- This context runs on real Postgres and real Kafka only (fleet rule); the
  shipped binary has no in-memory path.

## References

- Fleet plan: `~/warehouse-systems/.hermes/plans/2026-10-20_231017-returns-processing-and-exception-management.md` (decision log, pinned contracts, waves).
- Precedents: `inbound-receiving` ADR 0001 (receipt seam), `inventory-storage`
  ADR 0013 (event-fed local copies instead of synchronous reads).
