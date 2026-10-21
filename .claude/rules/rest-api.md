---
paths:
  - "internal/adapters/inbound/http/**"
  - "apis/openapi*.yaml"
  - "apis/openapi/**"
---

# REST API (inbound adapter)

Route -> use case, kept in sync with `apis/openapi.yaml`.

- `POST /returns`                                      -> `AuthorizeReturn` (201; 409 `return-not-authorizable`)
- `GET  /returns/{rmaId}`                              -> `GetReturn`
- `GET  /returns?orderRef=`                            -> `ListReturnsByOrder` (200 + `[]` on unknown, never 404)
- `POST /returns/{rmaId}/receipts`                     -> `OpenReturnReceipt` (201)
- `POST /receipts/{receiptId}/lines/{lineNo}/inspect`  -> `InspectReturnLine` (200; disposition decided in the same tx)
- `POST /dispositions/{id}/override`                   -> `OverrideDisposition` (200; reason mandatory)
- `PUT  /disposition-policies/{version}`               -> `DeclareDispositionPolicy` (201)
- `GET  /disposition-policies/current`                 -> `GetDispositionPolicy`
- `GET  /healthz`, `GET /readyz` (drain gate + Postgres ping + Kafka metadata), `GET /metrics`

## Conventions

- Error shape: RFC 7807 `application/problem+json`; problem types are kebab-case and listed in the OpenAPI spec.
- Every POST/PUT requires an `Idempotency-Key` header (middleware before the handler; a replay returns the stored response).
- A NUL byte in a path is 404, in a body 400 `malformed-request-body` (Postgres rejects U+0000).
- Auth: none. The fleet-wide REST+MCP bearer layer was reverted 2026-09-11; `TestNoAuthMiddlewareReintroduced` keeps it that way.
- No handler calls a sibling context over REST or MCP.
