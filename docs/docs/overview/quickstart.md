---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
sidebar_position: 4
---

# Quickstart

Run the service by hand against real Postgres and Kafka, then drive the whole reverse flow over
REST. Everything here uses the real runtime — there is no in-memory mode.

## Prerequisites

- Go 1.27+ (the module's toolchain), Docker for the database,
- a Kafka broker you may point `KAFKA_BROKERS` at (the fleet kind cluster's, or any local one).
  `docker-compose.yml` in the repo root starts only Postgres; Kafka comes from your environment.

## Make targets

| Target | What it does |
| --- | --- |
| `make check` | fmt-check + vet + build + lint + unit tests (the pre-push gate) |
| `make check-all` | `check` + coverage (90 % gate on domain + application) + arch fitness + BDD |
| `make integration` | testcontainers suite: real Postgres AND real Kafka, `-tags=integration` |
| `make bdd` | the godog/Gherkin acceptance scenarios |
| `make api-lint` | Spectral over `apis/openapi.yaml` + `apis/asyncapi.yaml` |

## Run locally

```bash
# 1. Postgres (the compose file maps it to localhost:5433)
docker compose up -d postgres

# 2. Build and boot the API
go build ./cmd/api
export DATABASE_URL='postgres://returns:returns@localhost:5433/returns?sslmode=disable'
export KAFKA_BROKERS='localhost:9092'      # your broker
export PRODUCT_CLASSIFICATION_CONSUMER_GROUP='local-returns-classes'
export ORDER_LINES_CONSUMER_GROUP='local-returns-lines'
export MANIFEST_CONSUMER_GROUP='local-returns-manifests'
./api
# api: listening (:8080); migrations ran at boot; the relay polls every 1s
```

A missing `DATABASE_URL`, `KAFKA_BROKERS` or any of the three consumer groups is a boot error
naming the variable — the process refuses to start half-configured.

### Seed the authorization facts

`POST /returns` only authorizes against a line that is allocated AND manifested. Those facts arrive
as events; publish the producers' exact CloudEvents on the consumed topics (integration test
`TestConsumersIntegration` in `internal/adapters/inbound/kafka/` is the executable example):

| Topic | Type | Effect |
| --- | --- | --- |
| `warehouse.order-management.events` | `com.warehouse.wes.order-management.order.OrderAllocated` | one row per (order, line) in `allocated_lines` |
| `warehouse.fulfillment.events` | `com.warehouse.wes.fulfillment-execution.package.PackageManifested` | `order_ref` `{order_id}-line-{line_no}` reversed into `manifested_units` |
| `warehouse.product-master.events` | `com.warehouse.wms.product-master.product.ProductClassified` | `product_classes` copy (version-guarded) |

### First calls

```bash
# Declare policy version 1 (append-only; the table maps grade x class to an outcome)
curl -i -X PUT localhost:8080/disposition-policies/1 \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: pol-1' -d '{
    "returnWindowDays": 30, "maxUnitsPerLine": 5, "vetoOutcome": "DISPOSE",
    "rules": [
      {"grade": "A", "productClass": "*", "outcome": "RESTOCK"},
      {"grade": "B", "productClass": "*", "outcome": "RESTOCK"},
      {"grade": "C", "productClass": "*", "outcome": "LIQUIDATE"},
      {"grade": "SCRAP", "productClass": "*", "outcome": "DISPOSE"}
    ]
  }'
# 201 {"version":1,...}

# Authorize a return for an allocated + manifested line
curl -i -X POST localhost:8080/returns \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: rma-1' -d '{
    "orderId": "ord-7c9e", "lineNo": 3, "sku": "SKU-1001",
    "quantity": 2, "siteId": "SITE-1", "reasonCode": "DAMAGED_BOX"
  }'
# 201 {"rmaId":"rma-...","status":"AUTHORIZED","windowEndsAt":"...","version":1}
# (a line with no manifest yet answers 409 .../return-not-authorizable, code LINE_NOT_MANIFESTED)

# Read it back, by id and by order
curl -s localhost:8080/returns/rma-... | jq
curl -s 'localhost:8080/returns?orderRef=ord-7c9e' | jq '.items | length'   # 1

# Receive the parcel against the RMA (every line is MATCHED, carrying its
# rmaId, or explicitly UNEXPECTED — there is no third state)
curl -i -X POST localhost:8080/returns/rma-.../receipts \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: rcpt-1' -d '{
    "siteId": "SITE-1",
    "lines": [{"lineNo": 1, "sku": "SKU-1001", "quantity": 2,
                "kind": "MATCHED", "rmaId": "rma-..."}]
  }'
# 201 {"receiptId":"rcpt-...",...}   (kind UNEXPECTED, no rmaId, raises
#                                     UnexpectedReturnReceived)

# Grade the line: the disposition is decided in the same transaction
# (quantity defaults to the line's full received quantity)
curl -i -X POST localhost:8080/receipts/rcpt-.../lines/1/inspections \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: insp-1' -d '{
    "grade": "A", "reasonCodes": ["OPENED"]
  }'
# 200 with the decision: outcome RESTOCK for grade A -> ReturnLineStaged is published

# Override it (reason mandatory; the original stays, superseded)
curl -i -X POST localhost:8080/dispositions/disp-.../overrides \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: ovr-1' -d '{
    "outcome": "REFURBISH", "reason": "customer story checked, cosmetic only"
  }'
# 200
```

Every write takes an `Idempotency-Key` header; replaying the same key and body returns the stored
answer, the same key with a different body is `409 idempotency-key-reused`
([API reference](/docs/api-reference)).

## Watch the events

```bash
kafka-console-consumer --bootstrap-server localhost:9092 \
  --topic warehouse.returns-processing.events --from-beginning
# ReturnAuthorized, ReturnReceiptOpened, ReturnLineInspected,
# DispositionDecided, ReturnLineStaged — CloudEvents 1.0 structured JSON
```

## Run the read-only MCP server

```bash
go build ./cmd/mcp
DATABASE_URL='postgres://returns:returns@localhost:5433/returns?sslmode=disable' ./mcp
# mcp server listening (Streamable HTTP) :8090 — tools: get_return,
# list_returns_by_order, get_disposition_policy  (details: /docs/mcp/tools)
```

## Build this documentation

```bash
cd docs
npm ci
npm run build     # static site in docs/build; npm run gen-api-docs regenerates
                  # the REST pages from ../apis/openapi.yaml (never hand-edit them)
```
