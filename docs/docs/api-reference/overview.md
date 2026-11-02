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
| Receipts | `POST /receipts/{receiptId}/lines/{lineNo}/inspections` | `InspectReturnLine` | `200` with the receipt line and the decided disposition (same transaction) |
| Dispositions | `POST /dispositions/{dispositionId}/overrides` | `OverrideDisposition` | `200`, the superseding disposition |
| Disposition policies | `PUT /disposition-policies/{version}` | `DeclareDispositionPolicy` | `201`; append-only |
| Disposition policies | `GET /disposition-policies/current` | `GetDispositionPolicy` | `200`, the current version |
| Health | `GET /healthz` | liveness | `200` |
| Health | `GET /readyz` | drain gate + Postgres ping + Kafka metadata | `200` / `503 dependency-unavailable` |

The service exports no runtime metrics, so the spec declares no `/metrics` route (see
[Observability](/docs/operations/observability)). The four `GET /reports/*` routes are served by
the separate `returns-reports` binary (`:8092`) and are bounded by `from`/`to`, so they are
unpaginated (`x-pagination-exempt`).

## Conventions

- **Bodies.** Strict JSON (`additionalProperties: false` everywhere): unknown fields are
  `400 malformed-request-body`. A NUL byte in a path is `404`, in a body `400`. JSON names are
  camelCase on REST and snake_case in event `data`.
- **`Idempotency-Key`** is required on every write — the four POSTs and the policy PUT. A missing
  key is `400 idempotency-key-required` (a malformed one `400 idempotency-key-invalid`); the same
  key, method, path and body replays the stored answer (4xx included) with
  `Idempotency-Replayed: true`; the same key with another method, path or body is
  `409 idempotency-key-reused`. A 5xx is never stored.
- **Pagination.** `GET /returns` is keyset-paged: `limit` (1–500, default 100) and an opaque
  `cursor`; the answer is `{items, nextCursor}` and `nextCursor` is absent on the last page. A bad
  `limit` is `400 invalid-limit`, an undecodable `cursor` `400 invalid-cursor`.
- **Versions.** Aggregate `version` guards every write: a lost update is
  `409 concurrent-modification`. The policy is append-only by `version` path segment; an RMA move
  and a receipt write bump their aggregates' versions.
- **Errors** are RFC 7807 `application/problem+json` with
  `type = https://errors.returns-processing.warehouse-systems.dev/<slug>`. The slugs:
  `malformed-request-body`, `invalid-quantity`, `invalid-line`, `invalid-grade`, `invalid-policy`,
  `idempotency-key-required`, `idempotency-key-invalid`, `idempotency-key-reused`, `invalid-limit`, `invalid-cursor`, `rma-not-found`, `receipt-not-found`,
  `disposition-not-found`, `policy-not-found`, `return-not-authorizable`, `open-rma-exists`,
  `rma-not-receivable`, `line-already-inspected`, `inspected-exceeds-received`,
  `override-reason-required`, `concurrent-modification`, `dependency-unavailable`,
  `internal-error`. Causes and fixes are on
  [Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).
- **Auth**: none (`TestNoAuthMiddlewareReintroduced` fails CI if auth middleware returns). No CORS
  middleware.
