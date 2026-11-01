# ADR 0009: Adopt the fleet REST API guideline

## Status

Accepted (2026-11-01). Breaking by design: it ships with the fleet's REST cutover set and is deployed
together with it.

## Context

The 2026-10-30 application portfolio assessment found the fleet's REST APIs diverging: two spellings
of the replay header, key reuse answered `422` in most services and `409` in one, three styles of
command URL (`:verb`, verb sub-paths, sub-resources) and unpaginated lists. The fleet standard
(Warehouse REST API Guideline, rules R1-R8, enforced by `.spectral.yaml` +
`.spectral/functions/restGuideline.js` in the `api-lint` job) removes every divergence.

## Decision

returns-processing adopts the guideline as it is written:

- **Nouns, not verbs.** A command that records a durable thing is a `POST` that creates that noun:
  - `POST /receipts/{receiptId}/lines/{lineNo}/inspect` -> `POST /receipts/{receiptId}/lines/{lineNo}/inspections`
  - `POST /dispositions/{dispositionId}/override` -> `POST /dispositions/{dispositionId}/overrides`

  `operationId`s, status codes and bodies do not change. The old routes are gone (no alias); a test
  (`TestRetiredRoutesAreGone`) pins that they answer 404/405.
- **Idempotent writes.** Every `POST` (and the policy `PUT`) sits behind the Idempotency-Key
  middleware (route-scoped; the key is global, fingerprint = method + path + SHA-256 of the body).
  A replay carries `Idempotency-Replayed: true` (the retired one-word-shorter spelling appears
  nowhere); the same key with another method, path or body is `409 idempotency-key-reused`; a missing
  key is `400 idempotency-key-required`, a key that is not 1-128 printable ASCII characters is
  `400 idempotency-key-invalid`. No POST was unkeyed before this change.
- **Keyset pagination.** `GET /returns?orderRef=` takes `limit` (1-500, default 100) and an opaque
  `cursor` and answers `{items, nextCursor}` ordered by (line number, RMA id) in both the Postgres and
  the in-memory repositories, with the fleet `page` package
  (`internal/adapters/inbound/http/page`). `400 invalid-limit` / `400 invalid-cursor`. The MCP tool and
  the A2A skill keep their own schemas and walk the pages inside the use case, so they still return
  complete data.
- **Spec drift removed.** `apis/openapi.yaml` now declares the four `GET /reports/*` routes the
  `returns-reports` binary serves, and no longer declares `GET /metrics`, which no handler ever
  served.

## Exemptions (STANDARD 3.5)

| Operation | Exemption | Reason |
| --- | --- | --- |
| `GET /reports/disposition-mix`, `/reports/value-recovery`, `/reports/return-cycle-time` | `x-pagination-exempt: report` | read models bounded by a `from`/`to` window (default 30 days, at most 366) and a fixed daily granularity |
| `GET /reports/freshness` | `x-pagination-exempt: view` | one composite resource (`as_of`, `lag_seconds`) |
| `GET /disposition-policies/current` | none needed | a single policy object, not a collection (the ruleset does not flag it) |

## Consumers changed

- The `returns_mfe` console remote (`web/src/api.ts`, `ReturnDetailScreen`, types and tests): the new
  routes, and `listReturns` follows `nextCursor` until it is absent.
- The inbound MCP tool and A2A skill read through `ListReturnsByOrder.Execute`, which now walks the
  keyset pages.
- `e2e-tests` calls the renamed routes from its own PR.

## Consequences

Clients of the two renamed routes and of `GET /returns` must be updated in the same deployment as
this service. The ruleset keeps the contract from drifting again.
