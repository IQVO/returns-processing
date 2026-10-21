# ADR 0003: event-fed local copies and the consumed contracts

## Status

Accepted (2026-10-21).

## Context

Authorizing a return needs three facts this context does not own: the line
was allocated (order-management), the line actually shipped
(fulfillment-execution's manifest), and the product's classification
(product-master, for the disposition policy). The fleet rule
(context-boundaries) forbids a live REST or MCP call between contexts, so
returns-processing keeps durable local copies fed by Kafka, the same stance
as `pick-face-replenishment` and `inventory-storage`. This ADR lists what is
consumed, verified against each producer's `origin/develop`
`apis/asyncapi.yaml` on 2026-10-20 (the fleet plan's pinned table).

## Decision

### Consumed contracts

| producer | topic | full type | used for | status |
|---|---|---|---|---|
| product-master | `warehouse.product-master.events` | `com.warehouse.wms.product-master.product.ProductClassified` | `product_classifications` copy (hazmat, DOT class, temperature, forced outcome) | Verified |
| order-management | `warehouse.order-management.events` | `com.warehouse.wes.order-management.order.OrderAllocated` | `allocated_order_lines` copy | Verified (lines carry NO quantity) |
| order-management | `warehouse.order-management.events` | `com.warehouse.wes.order-management.order.OrderPartiallyAllocated` | `allocated_order_lines` copy (only the allocated lines) | Verified (same shape) |
| fulfillment-execution | `warehouse.fulfillment.events` | `com.warehouse.wes.fulfillment-execution.package.PackageManifested` | `manifested_work_units` copy (`order_ref` reversed) | Verified |

Payloads are in `apis/asyncapi.yaml`, restated from the producers and never
imported. Every other type on these topics is ignored.

### Local copies

| table | fed by | rule |
|---|---|---|
| `product_classifications` | `ProductClassified` | one row per SKU; apply only when `version` is greater than the stored one |
| `allocated_order_lines` | `OrderAllocated`, `OrderPartiallyAllocated` | one row per (order_id, line_no); upsert the SKU on every event; a later partial allocation refreshes only its own lines |
| `manifested_work_units` | `PackageManifested` | one row per work unit: reverse `order_ref` = `{order_id}-line-{line_no}` into (order_id, line_no); insert when absent |

### Version and dedup semantics

- `ProductClassified.version` is per-SKU monotonically increasing: apply only
  when `version > stored`. The two order events carry no version: an
  allocation event is idempotent by its (order_id, line_no) key — the same
  allocation replayed writes the same row. `PackageManifested` is idempotent
  by the work unit key.
- Dedupe on the CloudEvents `id`, claimed in the SAME database transaction as
  the effect; the Kafka offset is committed only after success
  (`FetchMessage` + `CommitMessages`). A redelivered event with a seen `id`
  changes nothing and still commits the offset.
- Dispatch on the FULL `type` string; ignore unknown types; skip (WARN, commit
  past) anything that is not a valid CloudEvent — never crash, never block
  the partition, never fall back to parsing a legacy shape.
- Consumer group ids come from env, never a string literal at the call site:
  `PRODUCT_CLASSIFICATION_CONSUMER_GROUP`, `ORDER_LINES_CONSUMER_GROUP`,
  `MANIFEST_CONSUMER_GROUP`; a missing group is a boot error naming the
  variable.

### Authorization reads only its copies

`AuthorizeReturn` reads the three copies; an empty or stale copy produces a
rejection (`LINE_NOT_MANIFESTED`), never a guess and never a live call.

### Planned, not built (from ADR 0001 — never faked)

- **Shipped-quantity check per line.** `OrderAllocated` lines carry `line_no`,
  `sku`, `path_id`, `gift_wrap`, `fulfillment_class` and NO quantity, so the
  authorized quantity can only be capped by
  `DispositionPolicy.max_units_per_line` until order-management adds
  `quantity` to its released line (additive producer change, asked
  separately).
- **Restock loop closure.** inventory-storage's `ItemStowed` is not on its
  integration topic yet; when its promotion lands, this service can consume
  it to close the loop from `ReturnLineStaged` to a stowed bin. Until then a
  staged line's stow is invisible here, by design.

## Consequences

- Three consumer groups' worth of lag is the cost of keeping this service
  independent of its producers' availability.
- Until a producer event arrives, the matching copy is empty and
  authorizations fail closed (`LINE_NOT_MANIFESTED`) rather than open.
- The consumer-first restate means this repo pins the four contracts as they
  are on the producers' `origin/develop` today; if a producer changes one,
  the weekly fleet drift sensor fires and this ADR and `apis/asyncapi.yaml`
  are fixed in a follow-up PR.
