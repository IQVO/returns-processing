---
id: events
title: Event catalogue
sidebar_label: Event catalogue
sidebar_position: 99
---

# Event catalogue

Summary of `apis/asyncapi.yaml` (AsyncAPI 2.6.0, version 1.0.0). The file is the authority for
payload fields; [ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue) is the type
catalogue and [Domain events](/docs/ddd/domain-events) carries the field-level detail.

## Envelope

Every message, published or consumed, is a **CloudEvents 1.0** event in structured content mode
with the Kafka header `content-type: application/cloudevents+json; charset=UTF-8`.

| Attribute | Value |
| --- | --- |
| `specversion` | `1.0` |
| `id` | UUID minted once per domain event at encode time and persisted with the outbox row; consumers dedupe on it |
| `source` | `/warehouse/returns-processing` |
| `type` | `com.warehouse.wms.returns-processing.<entity>.<EventName>` with `<entity>` one of `returnauthorization`, `returnreceipt`, `disposition` |
| `subject` | the aggregate instance id (`rma-…`, `rcpt-…` or `disp-…`) |
| `time` | domain occurred-at, UTC |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:warehouse:returns-processing:events:<EventName>:v1` |

The Kafka key is the subject. A breaking payload change gets a new `.v2` type and dataschema; an
existing one is never mutated.

## Published

Topic `warehouse.returns-processing.events`, written by the outbox relay only
([ADR 0005](/docs/adr/0005-transactional-outbox-and-relay)). Every event is
also mirrored on `warehouse.returns-processing.analytics` (same `id`, same
payload, analytics dataschema), consumed only by this service's own projector
([ADR 0007](/docs/adr/0007-analytics-read-side); see
[Analytics](/docs/operations/analytics)).

| Type suffix | Raised | Payload (`data`) |
| --- | --- | --- |
| `returnauthorization.ReturnAuthorized` | `POST /returns` (a manifested, in-window line) | `rma_id`, `order_id`, `line_no`, `sku`, `quantity`, `site_id`, `reason_code`, `window_ends_at`, `authorized_at` |
| `returnauthorization.ReturnAuthorizationRejected` | `POST /returns` (a decided rejection) | `rma_id`, `order_id`, `line_no`, `sku`, `quantity`, `rejection_code`, `rejected_at` |
| `returnreceipt.ReturnReceiptOpened` | `POST /returns/{rmaId}/receipts` | `receipt_id`, `rma_id` (nullable), `site_id`, `opened_at`, `lines:[{line_no, sku, quantity, rma_id (nullable)}]` |
| `returnreceipt.UnexpectedReturnReceived` | a received line with no RMA | `receipt_id`, `line_no`, `sku`, `quantity`, `site_id`, `received_at` |
| `returnreceipt.ReturnLineInspected` | `POST /receipts/{receiptId}/lines/{lineNo}/inspections` | `receipt_id`, `line_no`, `sku`, `quantity`, `grade`, `reason_codes:[]`, `inspected_at` |
| `disposition.DispositionDecided` | inspection (policy) or `POST /dispositions/{id}/overrides` | `disposition_id`, `receipt_id`, `line_no`, `sku`, `quantity`, `grade`, `outcome`, `policy_version`, `supersedes` (nullable), `reason` (nullable), `decided_at` |
| `disposition.ReturnLineStaged` | exactly once per RESTOCK disposition | `receipt_id`, `line_no`, `sku`, `quantity`, `condition`, `received_at`, `rma_id`, `disposition_id`, `site_id` |

Downstream consumers (verified on their `origin/develop`, 2026-10-21): inventory-storage consumes
`ReturnLineStaged` into its staged-receipt use case (its ADR 0044; `disposition_id` is its
idempotency key); exception-management consumes `UnexpectedReturnReceived` to open a problem-solve
case. Outbox rows the encoder refuses are dead-lettered to
`warehouse.returns-processing.events.dlq`.

## Consumed

| Type | Topic | Consumer group env |
| --- | --- | --- |
| `com.warehouse.wms.product-master.product.ProductClassified` | `warehouse.product-master.events` | `PRODUCT_CLASSIFICATION_CONSUMER_GROUP` |
| `com.warehouse.wes.order-management.order.OrderAllocated` | `warehouse.order-management.events` | `ORDER_LINES_CONSUMER_GROUP` |
| `com.warehouse.wes.order-management.order.OrderPartiallyAllocated` | `warehouse.order-management.events` | `ORDER_LINES_CONSUMER_GROUP` |
| `com.warehouse.wes.fulfillment-execution.package.PackageManifested` | `warehouse.fulfillment.events` | `MANIFEST_CONSUMER_GROUP` |

Local-copy rules: one row per key (sku / (order, line) / work unit), apply only newer versions
where a version exists, insert when absent; dedupe on the CloudEvents `id` in the same transaction
as the effect; commit the offset only after success. Every other type on those topics commits past
untouched; anything that is not a valid CloudEvent is WARN-logged and skipped; a payload that can
never apply dead-letters to `<topic>.dlq` ([Runbook](/docs/operations/runbook#dead-letter-topics)).
