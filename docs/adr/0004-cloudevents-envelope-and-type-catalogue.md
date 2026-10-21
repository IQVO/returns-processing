# ADR 0004: CloudEvents envelope and type catalogue

## Status

Accepted (2026-10-21).

## Context

Every Kafka message in the fleet is a CloudEvents 1.0 event in structured mode
(fleet rule `.claude/rules/fleet/cloudevents.md`, warehouse-docs
`docs/strategic-design/event-standard-cloudevents.md`). This ADR is the
catalogue of every type returns-processing publishes or consumes;
`TestEventCatalogueMatchesContract` checks it against `apis/asyncapi.yaml`.

## Decision

### Envelope

- `specversion` `1.0`; `id` a UUID persisted with the outbox row (stable
  across relay retries; consumers dedupe on it); `source`
  `/warehouse/returns-processing`; `subject` the aggregate instance id
  (`rma-…`, `rcpt-…` or `disp-…`); `time` the occurred-at instant in UTC;
  `datacontenttype` `application/json`; `dataschema`
  `urn:warehouse:returns-processing:events:<EventName>:v1`.
- Kafka value = the JSON event format; header
  `content-type: application/cloudevents+json; charset=UTF-8`; Kafka key = the
  subject, so all events of one aggregate stay ordered on one partition.
- `data` is snake_case; optional fields are omitted when unset; timestamps are
  RFC 3339 UTC.
- Built and decoded only through `internal/adapters/kafka/cloudevents`.

### Published

`<entity>` is the raising aggregate, lowercase, no separators:
`returnauthorization`, `returnreceipt` or `disposition`. The stream column
says where the type goes: `events` is `warehouse.returns-processing.events`.
An analytics mirror (`warehouse.returns-processing.analytics`, same `type`
and `id` per occurrence, `dataschema`
`urn:warehouse:returns-processing:analytics:<EventName>:v1`) is **planned,
not built** — it arrives with this service's own analytics read side, a later
decision; nothing consumes that topic today, so no consumer can break.

| type | stream | raised when | `data` |
|---|---|---|---|
| `com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorized` | events (analytics planned) | an RMA is authorized against a manifested line inside the window | `{rma_id, order_id, line_no, sku, quantity, site_id, reason_code, window_ends_at, authorized_at}` |
| `com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorizationRejected` | events (analytics planned) | an RMA request is refused: `LINE_NOT_MANIFESTED`, `RETURN_WINDOW_EXPIRED`, `OPEN_RMA_EXISTS` or `QUANTITY_EXCEEDS_POLICY` | `{rma_id, order_id, line_no, sku, quantity, rejection_code, rejected_at}` |
| `com.warehouse.wms.returns-processing.returnreceipt.ReturnReceiptOpened` | events (analytics planned) | a parcel arrives; every line is matched to an RMA or explicitly unexpected | `{receipt_id, rma_id (nullable), site_id, opened_at, lines:[{line_no, sku, quantity, rma_id (nullable)}]}` |
| `com.warehouse.wms.returns-processing.returnreceipt.ReturnLineInspected` | events (analytics planned) | a receipt line is graded (exactly once; inspected qty <= received qty) | `{receipt_id, line_no, sku, quantity, grade (A\|B\|C\|SCRAP), reason_codes:[], inspected_at}` |
| `com.warehouse.wms.returns-processing.returnreceipt.UnexpectedReturnReceived` | events (analytics planned) | a received line has no RMA; exception-management opens a case | `{receipt_id, line_no, sku, quantity, site_id, received_at}` |
| `com.warehouse.wms.returns-processing.disposition.DispositionDecided` | events (analytics planned) | the pure function `Decide(grade, classification, policy)` decides a line, or an operator overrides | `{disposition_id, receipt_id, line_no, sku, quantity, grade, outcome (RESTOCK\|REFURBISH\|LIQUIDATE\|DISPOSE\|RTV), policy_version, supersedes (nullable), reason (nullable), decided_at}` |
| `com.warehouse.wms.returns-processing.disposition.ReturnLineStaged` | events (analytics planned) | exactly once per RESTOCK disposition: the handover to inventory-storage | `{receipt_id, line_no, sku, quantity, condition ("Good"), received_at, rma_id, disposition_id, site_id}` |

Payload rules:

- `ReturnLineStaged.data` is inbound-receiving's `ReceiptLineReceived` shape
  (`receipt_id`, `line_no`, `sku`, `quantity`, `condition`, `received_at`)
  plus `rma_id`, `disposition_id` and `site_id`; inventory-storage consumes it
  into its staged-receipt use case and the stow stays an RF action there. The
  `disposition_id` is the idempotency key downstream.
- `DispositionDecided.supersedes` and `reason` are null on a policy decision;
  an override is a NEW Disposition whose `supersedes` names the original and
  whose `reason` is mandatory.
- `ReturnReceiptOpened.lines[].rma_id` null means the line is UNEXPECTED; the
  receipt-level `rma_id` is null only when the whole parcel was unexpected.
  There is no silent third state.
- Subject and Kafka key: `rmaId` for the returnauthorization types,
  `receiptId` for the returnreceipt types, `dispositionId` for the
  disposition types.
- A breaking payload change is a new `.v2` type and dataschema, never a
  mutation of v1.

### Consumed (ADR 0003)

| type | topic | consumer group env |
|---|---|---|
| `com.warehouse.wms.product-master.product.ProductClassified` | `warehouse.product-master.events` | `PRODUCT_CLASSIFICATION_CONSUMER_GROUP` |
| `com.warehouse.wes.order-management.order.OrderAllocated` | `warehouse.order-management.events` | `ORDER_LINES_CONSUMER_GROUP` |
| `com.warehouse.wes.order-management.order.OrderPartiallyAllocated` | `warehouse.order-management.events` | `ORDER_LINES_CONSUMER_GROUP` |
| `com.warehouse.wes.fulfillment-execution.package.PackageManifested` | `warehouse.fulfillment.events` | `MANIFEST_CONSUMER_GROUP` |

Every other type on those topics is ignored. A message that is not a valid
CloudEvent is logged and skipped, never retried forever.

## Consequences

- Consumers need nothing but this catalogue and `apis/asyncapi.yaml`; there is
  no shared Go code.
- `ReturnLineStaged` is the only type that asks a sibling to do something
  (inventory-storage stages the line); `UnexpectedReturnReceived` is the only
  type that feeds exception-management. No other disposition outcome touches
  any sibling.
- The analytics mirror is declared planned, not built: when it ships it will
  carry the same `type` and `id` per occurrence, and this ADR gains an
  amendment rather than a rewrite.
