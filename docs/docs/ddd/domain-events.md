---
id: domain-events
title: Domain events
sidebar_label: Domain events
sidebar_position: 20
---

# Domain events

Every event this context publishes or consumes, with the full CloudEvents `type`. The contract is
`apis/asyncapi.yaml`; the envelope (`specversion` 1.0, `id`, `source`, `type`, `subject`, `time`,
`datacontenttype` `application/json`, `dataschema`) is built only by
`internal/adapters/kafka/cloudevents`. Payload fields are snake_case and timestamps are RFC 3339
UTC. The catalogue itself is [ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue);
`TestEventCatalogueMatchesContract` fails CI when the two drift.

## Published

`dataschema` is `urn:warehouse:returns-processing:events:<EventName>:v1`; `source` is
`/warehouse/returns-processing`; the Kafka header `content-type` is
`application/cloudevents+json; charset=UTF-8`; the Kafka key is the subject, so all events of one
aggregate stay ordered on one partition. Everything leaves through the transactional outbox
([ADR 0005](/docs/adr/0005-transactional-outbox-and-relay)).

| Event | Full type | Key and subject | Raised by |
| --- | --- | --- | --- |
| ReturnAuthorized | `com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorized` | rmaId | `AuthorizeReturn` (REST `POST /returns`) |
| ReturnAuthorizationRejected | `com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorizationRejected` | rmaId | the domain's `Reject`; no v1 publisher path — a refusal persists nothing (see the note below) |
| ReturnReceiptOpened | `com.warehouse.wms.returns-processing.returnreceipt.ReturnReceiptOpened` | receiptId | `OpenReturnReceipt` (REST `POST /returns/{rmaId}/receipts`) |
| UnexpectedReturnReceived | `com.warehouse.wms.returns-processing.returnreceipt.UnexpectedReturnReceived` | receiptId | `OpenReturnReceipt`, one per UNEXPECTED line |
| ReturnLineInspected | `com.warehouse.wms.returns-processing.returnreceipt.ReturnLineInspected` | receiptId | `InspectReturnLine` |
| DispositionDecided | `com.warehouse.wms.returns-processing.disposition.DispositionDecided` | dispositionId | `InspectReturnLine` (policy) or `OverrideDisposition` |
| ReturnLineStaged | `com.warehouse.wms.returns-processing.disposition.ReturnLineStaged` | dispositionId | a RESTOCK disposition, exactly once |

### Payloads

| Event | `data` fields |
| --- | --- |
| ReturnAuthorized | `rma_id`, `order_id`, `line_no`, `sku`, `quantity`, `site_id`, `reason_code`, `window_ends_at`, `authorized_at` |
| ReturnAuthorizationRejected | `rma_id`, `order_id`, `line_no`, `sku`, `quantity`, `rejection_code` (`LINE_NOT_MANIFESTED`, `RETURN_WINDOW_EXPIRED`, `OPEN_RMA_EXISTS`, `QUANTITY_EXCEEDS_POLICY`), `rejected_at` |
| ReturnReceiptOpened | `receipt_id`, `rma_id` (nullable), `site_id`, `opened_at`, `lines:[{line_no, sku, quantity, rma_id (nullable)}]` |
| UnexpectedReturnReceived | `receipt_id`, `line_no`, `sku`, `quantity`, `site_id`, `received_at` |
| ReturnLineInspected | `receipt_id`, `line_no`, `sku`, `quantity`, `grade` (`A`/`B`/`C`/`SCRAP`), `reason_codes:[]`, `inspected_at` |
| DispositionDecided | `disposition_id`, `receipt_id`, `line_no`, `sku`, `quantity`, `grade`, `outcome` (`RESTOCK`/`REFURBISH`/`LIQUIDATE`/`DISPOSE`/`RTV`), `policy_version`, `supersedes` (nullable), `reason` (nullable), `decided_at` |
| ReturnLineStaged | `receipt_id`, `line_no`, `sku`, `quantity`, `condition` (`"Good"`), `received_at`, `rma_id`, `disposition_id`, `site_id` |

Publisher-path note: `ReturnAuthorizationRejected` is raised by the domain's `Reject` and sits in
the catalogue (ADR 0004), but **no v1 code path publishes it**: `AuthorizeReturn` decides
synchronously in one transaction and a refusal returns the typed domain error — nothing is
persisted, nothing is published. The event gains a publisher with a future async decision path
(ADR 0002 keeps `REQUESTED` for it).

Payload rules: `ReturnLineStaged.data` is inbound-receiving's `ReceiptLineReceived` shape plus
`rma_id`, `disposition_id`, `site_id` — inventory-storage consumes it into its staged-receipt use
case (its ADR 0044), and `disposition_id` is the downstream idempotency key.
`DispositionDecided.supersedes`/`reason` are null on a policy decision and mandatory on an
override. A breaking payload change is a new `.v2` type and dataschema, never a mutation of v1.

An analytics mirror (`warehouse.returns-processing.analytics`, same `type` and `id` per occurrence)
is **built** ([ADR 0007](/docs/adr/0007-analytics-read-side)): the relay writes both messages in
one pass and the service's own projector is the consumer.

Consumers of the published events, checked on each sibling's `origin/develop` (2026-10-21):
inventory-storage consumes `ReturnLineStaged` (built, its ADR 0044); exception-management consumes
`UnexpectedReturnReceived` to open a problem-solve case (built on its develop). Nothing else
consumes the topic today.

## Consumed

| Full type | Topic | Key | Fields used | Handler |
| --- | --- | --- | --- | --- |
| `com.warehouse.wms.product-master.product.ProductClassified` | `warehouse.product-master.events` | sku | `sku`, `handling_tags`, `temperature_class`, `dot_hazard_class`, `version` (+ forced outcome) | classifications consumer, `ApplyProductClassification` — apply only when `version >` stored |
| `com.warehouse.wes.order-management.order.OrderAllocated` | `warehouse.order-management.events` | orderId | `order_id`, `lines[{line_no, sku, ...}]` — NO quantity | order lines consumer, `ApplyAllocations` — one claim per event covering every line |
| `com.warehouse.wes.order-management.order.OrderPartiallyAllocated` | `warehouse.order-management.events` | orderId | same shape, only the allocated lines | order lines consumer — refreshes only its own lines |
| `com.warehouse.wes.fulfillment-execution.package.PackageManifested` | `warehouse.fulfillment.events` | packageId | `package_id`, `order_ref` (reversed `{order_id}-line-{line_no}`), envelope `time` → `manifested_at` | manifest consumer, `ApplyManifest` |

Every other type on those topics is ignored (commit past); a message that is not a valid
CloudEvent is WARN-logged and skipped; a payload that can never apply is poison and dead-letters.
Dedupe on the CloudEvents `id` — the claim is written in the same transaction as the effect, and
the offset commits only after success. Consumer groups:
`PRODUCT_CLASSIFICATION_CONSUMER_GROUP`, `ORDER_LINES_CONSUMER_GROUP`, `MANIFEST_CONSUMER_GROUP`.

## Example: ReturnLineStaged

This is the golden file `internal/adapters/outbound/kafka/testdata/return_line_staged.golden.json`
— the exact bytes the encoder test pins (one golden file per published type):

```json
{
  "specversion": "1.0",
  "id": "4e7c2f9b-6a13-4d58-9c8e-3b5f1a2d7e94",
  "source": "/warehouse/returns-processing",
  "type": "com.warehouse.wms.returns-processing.disposition.ReturnLineStaged",
  "subject": "disp-1",
  "time": "2026-10-21T10:06:00Z",
  "datacontenttype": "application/json",
  "dataschema": "urn:warehouse:returns-processing:events:ReturnLineStaged:v1",
  "data": {
    "receipt_id": "rcpt-1",
    "line_no": 1,
    "sku": "SKU-BOOK-0001",
    "quantity": 2,
    "condition": "Good",
    "received_at": "2026-10-21T10:00:00Z",
    "rma_id": "rma-1",
    "disposition_id": "disp-1",
    "site_id": "site-1"
  }
}
```
