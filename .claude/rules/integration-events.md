---
paths:
  - "internal/adapters/**/kafka/**"
  - "internal/adapters/outbound/events/**"
  - "apis/asyncapi*"
---

# Cross-service integration events (Kafka)

This service PUBLISHES on `warehouse.returns-processing.events` (integration)
and `warehouse.returns-processing.analytics` (its own projector only), and
CONSUMES `warehouse.product-master.events`, `warehouse.order-management.events`
and `warehouse.fulfillment.events` into local read models. It never calls a
sibling over REST or MCP. Type prefix `com.warehouse.wms.returns-processing.`,
source `/warehouse/returns-processing`.

## Events: CloudEvents 1.0 is MANDATORY

Every Kafka message this service produces or consumes (integration
`warehouse.<ctx>.events` AND analytics `warehouse.<ctx>.analytics`) is a
CloudEvents 1.0 event in structured content mode. This is a hard fleet rule,
not a preference — there is nothing to "choose" here:

- No flat envelope (`event_id`/`event_type`/`occurred_at`), no dual-write,
  no dual-read, no envelope toggle env var (`EVENT_ENVELOPE_MODE` is gone
  fleet-wide). `internal/architecture/fitness_test.go`'s
  `TestNoEventEnvelopeToggleOrFlatEnvelope` fails CI on any of them.
- Build/validate/(un)marshal with `github.com/cloudevents/sdk-go/v2/event`
  (latest v2) via ONE helper package,
  `internal/adapters/kafka/cloudevents/` — copy it from this template's
  `templates/cloudevents/cloudevents.go.tmpl` (+ its test) and change only
  the per-repo constants. Transport stays `segmentio/kafka-go` (no sdk-go
  protocol/client packages, no hand-rolled CloudEvent structs).
- Every produced message carries the Kafka header
  `content-type: application/cloudevents+json; charset=UTF-8`
  (`cloudevents.ContentTypeHeader()`), next to the W3C trace headers
  (`traceparent`/`tracestate` stay in headers, never duplicated into
  extension attributes). Message key = aggregate id, `kafkago.Hash{}`
  balancer.
- Required attributes: `specversion=1.0`; `id` (UUID v4 minted ONCE per
  domain event and persisted with the outbox row, so redelivery carries
  the same id); `source=/warehouse/<repo>`; `type`; `subject` (aggregate
  instance id, never empty); `time` (domain occurred-at, UTC);
  `datacontenttype=application/json`;
  `dataschema=urn:warehouse:<repo>:<events|analytics>:<EventName>:v<N>`.
  No custom extension attributes without an ADR.
- `type` = `com.warehouse.<subdomain>.<bounded-context>.<entity>.<EventName>`
  (this repo: `com.warehouse.wms.returns-processing`). The SAME `type` names the
  occurrence on both the integration and the analytics topic; `dataschema`
  names the payload shape. Breaking payload change => new `.v2` type + new
  dataschema version, never mutate an existing one.
- Consumers decode with `cloudevents.Decode` (validates), dispatch on the
  FULL `type` string (never a short name or suffix match), ignore unknown
  types, read `time`/`subject` from attributes and the payload via
  `DataAs`, dedupe on `id`, and DLQ/skip — WARN log + commit past, never
  crash, never block the partition, never fall back to parsing a legacy
  shape — anything that fails CloudEvents validation.
- Tests: a golden exact-JSON test per published `type` (all attributes +
  the `content-type` header); a legacy-flat-message-rejected test per
  consumer; Kafka integration tests via testcontainers only.

Full standard, subdomain table and the fleet's cross-service type
catalogue: warehouse-docs `docs/strategic-design/event-standard-cloudevents.md`.
Record it in this repo as its own ADR "CloudEvents 1.0 as the mandatory
event envelope" under `docs/docs/adr/`.

### Published types

| `type` | topic(s) | `subject` / key | `dataschema` |
| --- | --- | --- | --- |
| `com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorized` | `warehouse.returns-processing.events` (+analytics) | rmaId | `urn:warehouse:returns-processing:events:ReturnAuthorized:v1` |
| `com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorizationRejected` | same | rmaId | `urn:warehouse:returns-processing:events:ReturnAuthorizationRejected:v1` |
| `com.warehouse.wms.returns-processing.returnreceipt.ReturnReceiptOpened` | same | receiptId | `urn:warehouse:returns-processing:events:ReturnReceiptOpened:v1` |
| `com.warehouse.wms.returns-processing.returnreceipt.ReturnLineInspected` | same | receiptId | `urn:warehouse:returns-processing:events:ReturnLineInspected:v1` |
| `com.warehouse.wms.returns-processing.returnreceipt.UnexpectedReturnReceived` | same | receiptId | `urn:warehouse:returns-processing:events:UnexpectedReturnReceived:v1` |
| `com.warehouse.wms.returns-processing.disposition.DispositionDecided` | same | dispositionId | `urn:warehouse:returns-processing:events:DispositionDecided:v1` |
| `com.warehouse.wms.returns-processing.disposition.ReturnLineStaged` | same | dispositionId | `urn:warehouse:returns-processing:events:ReturnLineStaged:v1` |

`ReturnLineStaged.data` = `ReceiptLineReceived`'s fields (`receipt_id`, `line_no`, `sku`, `quantity`, `condition`, `received_at`) plus `rma_id`, `disposition_id`, `site_id`; inventory-storage consumes it into its staged-receipt use case.

### Consumed types

| `type` | topic | producer |
| --- | --- | --- |
| `com.warehouse.wms.product-master.product.ProductClassified` | `warehouse.product-master.events` | product-master |
| `com.warehouse.wes.order-management.order.OrderAllocated` | `warehouse.order-management.events` | order-management |
| `com.warehouse.wes.order-management.order.OrderPartiallyAllocated` | `warehouse.order-management.events` | order-management |
| `com.warehouse.wes.fulfillment-execution.package.PackageManifested` | `warehouse.fulfillment.events` | fulfillment-execution (`order_ref` = work unit `{order_id}-line-{line_no}`) |

Consumer groups (env, no default, boot error when unset): `PRODUCT_CLASSIFICATION_CONSUMER_GROUP`, `ORDER_LINES_CONSUMER_GROUP`, `MANIFEST_CONSUMER_GROUP`.

## Consumer group id

If this service consumes Kafka: state where the consumer group id comes
from. It MUST be env-configurable, never a hardcoded string literal --
`internal/architecture/fitness_test.go`'s
TestKafkaConsumerGroupNeverHardcodedInline enforces this (a real incident:
wes-work-planning's hardcoded group id let a locally-run e2e-tests process
silently collide with the live in-cluster Deployment's consumer group on
the shared fleet Kafka broker).

If this consumer replays from FirstOffset on every start to build an
in-memory read model (rather than resuming from a committed offset), the
group id must additionally be UNIQUE PER PROCESS INSTANCE (hostname+PID+
timestamp), not just configurable -- see HARNESS.md's Kafka section for
why a shared group breaks that pattern specifically.
