---
id: troubleshooting
title: Troubleshooting
sidebar_label: Troubleshooting
sidebar_position: 4
---

# Troubleshooting

## Process and boot

| Symptom | Cause | Fix |
| --- | --- | --- |
| `invalid configuration: DATABASE_URL is required: this service runs on real Postgres` (exit 1) | no DSN | set `DATABASE_URL`; there is no fallback runtime |
| `... KAFKA_BROKERS is required ...` | no brokers | set `KAFKA_BROKERS`; Kafka is mandatory, not optional |
| `... PRODUCT_CLASSIFICATION_CONSUMER_GROUP is required ...` (same for `ORDER_LINES_`, `MANIFEST_`) | a pinned group id is missing | set it; the name is pinned in `.claude/rules/integration-events.md` |
| Boot hangs/loops on migrations behind PgBouncer | golang-migrate's advisory lock is session-scoped; PgBouncer transaction pooling cannot honour it | set `MIGRATIONS_DATABASE_URL` to a direct session DSN |
| Pod restarts with `service exited with error` naming a consumer | a consumer died (any error but a clean cancel) — deliberate: a half-alive service on frozen local copies is worse | read the consumer's error line above it; usually broker unreachable or a poison loop |

## Events not flowing out

| Symptom | Cause | Fix |
| --- | --- | --- |
| `outbox` table growing, nothing on `warehouse.returns-processing.events` | broker unreachable: rows back off, the relay pass stops | fix broker reachability (`/readyz` says `not ready: kafka`); rows drain themselves, oldest first |
| ERROR `row reached the attempt cap ... re-arm it` | one row failed `OUTBOX_MAX_ATTEMPTS` (default 20) times and is excluded from claims | fix the cause, then `UPDATE outbox SET attempts = 0, next_attempt_at = now() WHERE id = '<uuid>'` |
| WARN `dead-lettered a row the encoder refuses` | the row can never be encoded (unknown event, malformed payload) — deterministic | inspect it on `warehouse.returns-processing.events.dlq`; not retryable by design |
| Message on the DLQ only | it is poison, and the message behind it still applied | verify the effect you expected from the NEXT message, not this one |

## Events not flowing in (local copies empty)

| Symptom | Cause | Fix |
| --- | --- | --- |
| `POST /returns` answers `409 return-not-authorizable`, code `LINE_NOT_MANIFESTED` | the (order, line) has no manifested work unit yet — or the manifest/allocation consumer never ran | check the consumer groups are consuming (`warehouse.fulfillment.events`, group `MANIFEST_CONSUMER_GROUP`); authorizations fail CLOSED until the facts arrive, by design |
| `ProductClassified` applied once, then never again | version gate: only `version >` stored applies | correct behaviour (ADR 0003); an older event is a store-no-op |
| WARN `not a valid CloudEvent` in the logs | a flat envelope or garbage on a consumed topic | skipped and committed past by design; find the producer of that message |
| Same event applied twice? | it cannot: the CloudEvents `id` is claimed in the same transaction as the effect | check `processed_events` for the id if you suspect otherwise |

## Decisions look wrong

| Symptom | Cause | Fix |
| --- | --- | --- |
| Grade A answered `DISPOSE`, not `RESTOCK` | a guardrail demoted it: the SKU is DISPOSE-classified, or hazmat without a DOT rating | that is fail-closed by design (ADR 0002); check `get_disposition_policy` and the SKU's classification |
| RESTOCK for grade C | impossible from `Decide` | an operator override did it — check the disposition's `supersedes`/`reason` |
| `409 line-already-inspected` | a line is graded exactly once | correct the DECISION with an override, never a re-inspection |
| `422 override-reason-required` | the reason field is mandatory (1–500 chars) | send it; the override explains itself |
| Two dispositions, one line | the second carries `supersedes` = the first | the ORIGINAL is immutable — audit trail, not a bug |

## REST problem types

All errors are RFC 7807 with
`type = https://errors.returns-processing.warehouse-systems.dev/<slug>`:

| Slug | Meaning / fix |
| --- | --- |
| `malformed-request-body` | not strict JSON, unknown field, or a NUL byte — send exactly the documented fields |
| `invalid-quantity` / `invalid-line` / `invalid-grade` / `invalid-policy` | value out of range or not in the enum |
| `idempotency-key-required` | every write (4 POSTs + the policy PUT) needs an `Idempotency-Key` header |
| `idempotency-key-reused` (`409`) | the same key was sent with a DIFFERENT method, path or body — one key per logical request |
| `rma-not-found` / `receipt-not-found` / `disposition-not-found` / `policy-not-found` | unknown id (or no policy declared yet) |
| `return-not-authorizable` | rejection code in the body: `LINE_NOT_MANIFESTED`, `RETURN_WINDOW_EXPIRED`, `OPEN_RMA_EXISTS`, `QUANTITY_EXCEEDS_POLICY` |
| `open-rma-exists` | one open RMA per (order, line); the existing one must close first |
| `rma-not-receivable` | a receipt was opened against an RMA that is not in AUTHORIZED state |
| `line-already-inspected` / `inspected-exceeds-received` | grading rules (ADR 0002) |
| `override-reason-required` | an override must explain itself |
| `concurrent-modification` | optimistic version guard lost — reload, re-apply |
| `dependency-unavailable` | Postgres or Kafka unreachable behind `503 /readyz` — not a client error |
| `internal-error` | anything unexpected; the log line carries the cause |

## Idempotency-Key behaviour worth knowing

The FIRST answer for a key is stored and replayed — including 4xx problem documents. A 5xx is never
stored (safe to retry the same key). Replaying a stored `201` does NOT create a second RMA, receipt
or disposition; the stored body is returned verbatim.
