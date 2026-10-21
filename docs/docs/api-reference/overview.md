---
id: overview
title: API overview
sidebar_label: API overview
slug: /api-reference
sidebar_position: 1
---

# API overview

| Interface | Source of truth | Notes |
| --- | --- | --- |
| REST | `apis/openapi.yaml` (OpenAPI 3.0.3, version 1.0.0) | The pages under *REST* are generated from the spec with `npm run gen-api-docs`; do not edit them by hand. Servers declared in the spec: `http://localhost:8080` (local) and `http://localhost:8000/api/returns-processing` (Kong in the local kind cluster; not wired in `warehouse-infra` yet). |
| Events | `apis/asyncapi.yaml` | Summarised on the [event catalogue](/docs/api-reference/events); every field on [Domain events](/docs/ddd/domain-events). |
| MCP | `internal/adapters/inbound/mcp` | `cmd/mcp`, Streamable HTTP on `:8090` at `/mcp`: read-only tools `get_return`, `list_returns_by_order`, `get_disposition_policy` ([MCP tools](/docs/mcp/tools)). Writes stay on REST. |

## Endpoints

| Tag | Endpoint | Use case | Success |
| --- | --- | --- | --- |
| Returns | `POST /returns` | `AuthorizeReturn` | `201` + `Location`; a decided rejection is `409 return-not-authorizable` with a rejection code |
| Returns | `GET /returns?orderRef=` | `ListReturnsByOrder` | `200 {items}`; unknown order answers `200` with `[]`, never `404` |
| Returns | `GET /returns/{rmaId}` | `GetReturn` | `200`, the RMA |
| Receipts | `POST /returns/{rmaId}/receipts` | `OpenReturnReceipt` | `201` + `Location` |
| Receipts | `POST /receipts/{receiptId}/lines/{lineNo}/inspect` | `InspectReturnLine` | `200` with the receipt line and the decided disposition (same transaction) |
| Dispositions | `POST /dispositions/{dispositionId}/override` | `OverrideDisposition` | `200`, the superseding disposition |
| Disposition policies | `PUT /disposition-policies/{version}` | `DeclareDispositionPolicy` | `201`; append-only |
| Disposition policies | `GET /disposition-policies/current` | `GetDispositionPolicy` | `200`, the current version |
| Health | `GET /healthz` | liveness | `200` |
| Health | `GET /readyz` | drain gate + Postgres ping + Kafka metadata | `200` / `503 dependency-unavailable` |

`GET /metrics` is **declared in the spec but not served by the binary** — no metrics handler is
wired today. The generated page for it exists because the generator mirrors the spec; treat the
spec entry as aspirational until the exposition lands (see
[Observability](/docs/operations/observability)).

## Conventions

- **Bodies.** Strict JSON (`additionalProperties: false` everywhere): unknown fields are
  `400 malformed-request-body`. A NUL byte in a path is `404`, in a body `400`. JSON names are
  camelCase on REST and snake_case in event `data`.
- **`Idempotency-Key`** is required on every write — the four POSTs and the policy PUT. A missing
  key is `400 idempotency-key-required` (surfaced as a problem document); the same key and body
  replays the stored answer (4xx included); the same key with another body is
  `422 idempotency-key-reused`. A 5xx is never stored.
- **Versions.** Aggregate `version` guards every write: a lost update is
  `409 concurrent-modification`. The policy is append-only by `version` path segment; an RMA move
  and a receipt write bump their aggregates' versions.
- **Errors** are RFC 7807 `application/problem+json` with
  `type = https://errors.returns-processing.warehouse-systems.dev/<slug>`. The slugs:
  `malformed-request-body`, `invalid-quantity`, `invalid-line`, `invalid-grade`, `invalid-policy`,
  `idempotency-key-required`, `idempotency-key-reused`, `rma-not-found`, `receipt-not-found`,
  `disposition-not-found`, `policy-not-found`, `return-not-authorizable`, `open-rma-exists`,
  `rma-not-receivable`, `line-already-inspected`, `inspected-exceeds-received`,
  `override-reason-required`, `concurrent-modification`, `dependency-unavailable`,
  `internal-error`. Causes and fixes are on
  [Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).
- **Auth**: none (`TestNoAuthMiddlewareReintroduced` fails CI if auth middleware returns). No CORS
  middleware.
