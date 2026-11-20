# ADR 0013: retry, under the same Idempotency-Key, the refusals the network releases (replay class RETRYABLE)

## Status

Accepted (2026-11-19). Completes ADR 0012 for the outcome submission: it replaces "a definitive
refusal (400/404/409/422) is recorded FAILED, terminal" by "a refusal the network releases keeps the
outcome pending". It is the consumer half of the fleet idempotency rule of 2026-11-18
(`.claude/rules/fleet/idempotency-and-outbox.md`) and answers the Consequences of
vendor-connectivity ADR 0015, which names this service as a deterministic-key caller.

## Context

`POST /vendor/return-outcomes` carries the key `return-outcome-<authorizationId>`
(`internal/adapters/outbound/retailnetwork/returns_client.go`) and the same body on every attempt.
The client treated every 400/404/409/422 as a verdict (`ports.ErrOutcomeRefused`) and the drain
recorded the outcome FAILED. That was right while the network stored every refusal for 24 h: a retry
under the same key only replayed it.

vendor-connectivity (PR #93, its ADR 0015) now declares a replay class on every problem type. A
RETRYABLE refusal committed nothing, depends on state that can change, is NOT stored and releases the
key: the same request under the same key executes again and may pass. For this route that is, above
all, `404 not-found` (the return advice is an event-fed fact the network has not consumed yet),
`409 illegal-transition` (the advice is not yet in the state the outcome needs), `409
concurrent-update` (another writer won the optimistic lock) and `409 vendor-suspended` (an operator
reinstates the vendor). Recording these FAILED loses an outcome that would have been accepted a
minute later, and a FAILED outcome is never retried.

## Decision

1. **The class is the network's knowledge and lives in the anti-corruption layer.**
   `internal/adapters/outbound/retailnetwork/refusal_classes.go` holds one visible table,
   `returnOutcomeRefusals`, from the problem slug (`type` of the problem document) to retryable or
   terminal, derived from the network's class table (its ADR 0015). The use case and the domain only
   see `ports.OutcomeRefusal` (`ProblemSlug()`, `Released()`); nothing above the adapter names a slug
   table. The table is read for the refusals of `POST /vendor/return-outcomes` only: a refusal of a
   read keeps the verdict it always had.
2. **Unknown is terminal.** A slug that is not in the table, a 4xx with no problem document and a
   body that is not a problem document are terminal, exactly as before: an unknown refusal never
   loops. A 429 and a 5xx were never verdicts and stay plain errors the next tick retries.
3. **A released refusal keeps the outcome PENDING.** The drain does not record FAILED; it notes the
   refusal and the next tick submits the same outcome again, same body, same key. It never changes
   the key, so a retry that races a first answer the network did store (a 2xx) replays that answer.
   A released refusal is not an error of the tick: it is reported in `OutcomeDrainResult.Released`
   and logged at WARN (`authorization_id`, `refusal`).
4. **The operator sees the last refusal in an existing field.** `NoteOutcomeRefusal` writes the
   refusal text (status, slug, the callee's detail) into `return_advice_view.outcome_errors` while the
   outcome stays PENDING; the next accepted submission clears it, a terminal refusal replaces it
   (as `MarkOutcomeFailed` does today). No column, no migration, no REST/MCP/A2A/CloudEvents change.
5. **The retry is bounded and ends where it ended before.** There is no attempt counter in the view
   and none is added (that would be a migration). The bound is time: a released refusal keeps the
   outcome pending only while `now - PendingSince <= 24 h` (`usecases.DefaultReleasedRefusalWindow`,
   `SubmitReturnOutcomes.RetryWindow`); the first released refusal after the window is recorded
   `FAILED` with its text, the same terminal state and the same field as a final refusal today.
   `PendingSince` is the instant the outcome was composed, so the window includes any earlier
   transport outage. Transport errors and 5xx keep their current behaviour (retried every tick, not
   counted).
6. **The table is held against the network's contract.** `internal/adapters/outbound/retailnetwork/testdata/vendor-connectivity.openapi.yaml`
   is a byte-pinned copy of the network's `apis/openapi.yaml` (a pin of `contracts/pinned.json`,
   checked against its `develop` by the `pinned-contracts` CI job). `refusal_spec_test.go` reads from
   it, for `POST /vendor/return-outcomes`, the problem types the `Idempotency-Key` text says release
   the key and every problem type its 400/404/409/422 responses name, and fails when: a slug the spec
   names is unclassified; a released slug is not mapped retryable; or a slug is mapped retryable that
   the spec does not release (a stored refusal would only be replayed). The test also fails when the
   spec is reworded so that it can no longer read the list, and a second test proves the audit
   catches a dropped entry, a flipped class and a stale exception. The pin does not carry the network's
   class table (that is its ADR), so a slug no text names is held by the table's own entries.

### The table

| Slug | Status | Network class | Here | Why |
| --- | --- | --- | --- | --- |
| `not-found` | 404 | RETRYABLE | **retry** | the return advice is event-fed and may not be consumed yet |
| `illegal-transition` | 409 | RETRYABLE | **retry** | the advice has not reached `AUTHORIZED` yet |
| `concurrent-update` | 409 | RETRYABLE | **retry** | the loser of an optimistic lock re-reads and may win |
| `vendor-suspended` | 409 | RETRYABLE | **retry** | a reinstatement lets the same request pass |
| `node-not-owned-by-vendor`, `already-acknowledged`, `label-before-acceptance`, `confirm-without-label`, `ack-window-out-of-range` | 404/409/422 | RETRYABLE | retry | not answered by this route today; classified as the network classifies them |
| `validation-failed` | 422 | RETRYABLE | terminal | see below |
| `partial-acknowledgement`, `ack-window-closed` | 422/409 | FINAL | terminal | stored and replayed |
| `idempotency-key-required`, `idempotency-key-invalid`, `idempotency-key-reused` | 400/409 | not stored | terminal | this service sends a valid key and the same body every time |
| any other slug, no problem document | 400/404/409/422 | unknown | terminal | an unknown refusal never loops |

**`validation-failed` is the one deliberate divergence.** The network classes it RETRYABLE because one
slug answers both a malformed body and a state-dependent check (its ADR 0015, judgement 1). On this
route every cause of it is a function of the request (`Validate`, `Normalise`, the node or vendor
named not being the advice's), and the request is byte-identical on every attempt, so a retry cannot
pass; retrying would only delay the same FAILED by the window. It is kept terminal, recorded in
`releasedButTerminalHere` of the spec test with its reason, which fails the day the pin stops
releasing it or the table maps it retryable. If the network ever lets a state-dependent cause reach
this route, the entry flips to retry and the test follows.

## Consequences

- An outcome the network refuses with `not-found`, `illegal-transition`, `concurrent-update` or
  `vendor-suspended` is no longer lost: it stays PENDING and is submitted again every drain tick
  (`NETWORK_RETURNS_POLL_INTERVAL`, default 30 s) until accepted or until 24 h after it was composed.
  Past that it is FAILED with the last refusal, as every refusal was before.
- A vendor that stays suspended for more than a day, or an advice the network never consumes, still
  ends FAILED; the operator reads the refusal in `outcome_errors` and the WARN line while it retries.
- One more tick of load per refused outcome (a released refusal is one more execution at the network,
  by design of its class).
- The table is a copy of knowledge the network owns: a change of its class table is a change of this
  table. The pinned OpenAPI and its test are the early warning for the slugs the OpenAPI names; the
  pin is refreshed by copying the network's file (drift SLA of ADR 0003).
- `ports.ReturnAdviceFeed` gains `NoteOutcomeRefusal` (an internal port, both stores implement it,
  `repocontract.ReturnAdvices` covers it). No wire contract changes.
- Not done: an attempt counter (needs a column), an `Retry-After`-aware backoff, and an operator
  action to re-arm a FAILED outcome; each is its own decision.
