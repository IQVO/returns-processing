# ADR 0002: the ReturnAuthorization, ReturnReceipt and Disposition aggregates, their invariants, and the DispositionPolicy as data

## Status

Accepted (2026-10-21).

## Context

ADR 0001 gives this context the whole reverse flow: authorize, receive,
grade, decide. Order lines, shipments and product classifications belong to
other contexts and arrive as events (ADR 0003); they are read models here,
never aggregate state and never a live lookup. This ADR fixes the aggregates,
their state machines and invariants, and the shape of the policy object that
decides every disposition.

## Decision

Domain code lives under `internal/domain/` and imports nothing outside
`internal/domain/`. Four packages: `returnauthorization`, `returnreceipt`,
`disposition` (the richest aggregate, the mutation-gate package) and
`shared` (validated ids, `Quantity`, `SiteID` and the work-unit formula).

### ReturnAuthorization (package `returnauthorization`)

Permission to send a shipped order line back; identified by `rmaId`; carries
the authorized quantity, the return reason and the policy window.

State machine, exactly as `.claude/rules/domain-model.md` states it:

```
REQUESTED -> AUTHORIZED | REJECTED
AUTHORIZED -> RECEIVED
RECEIVED -> CLOSED
```

`REJECTED` and `CLOSED` are terminal: no transition leaves them. `Version`
is 1 when the RMA is decided and +1 per accepted transition; it guards every
write (409 on a lost update).

Invariants (each with a failing-path test in the package):

1. **One open RMA per (orderRef, lineNo).** The rule spans RMAs, so the
   aggregate is told the id of any open RMA for the line: a non-empty value
   is refused with `ErrOpenRMAExists` (and the use case backs it with a
   partial unique index on open RMAs, wave 2). "Open" = REQUESTED,
   AUTHORIZED or RECEIVED.
2. **Authorizable only when a manifested work unit exists for that line and
   now <= window end.** The manifested fact is the local copy of
   `PackageManifested`, keyed by the work unit `{order_id}-line-{line_no}`
   (`shared.OrderLineRef.WorkUnit()`); the window is
   `authorized_at + policy.return_window_days`. A blank work unit, a work
   unit that does not equal the line's own, or a now past the window end is
   refused.
3. **Quantity in `[1, policy.max_units_per_line]`.** The shipped-quantity
   check is planned, not built (order-management's allocated lines carry no
   quantity, ADR 0003); until it ships, the policy cap is the only ceiling.

Events: `ReturnAuthorized`, `ReturnAuthorizationRejected` (rejection codes
`LINE_NOT_MANIFESTED`, `RETURN_WINDOW_EXPIRED`, `OPEN_RMA_EXISTS`,
`QUANTITY_EXCEEDS_POLICY`).

### ReturnReceipt (package `returnreceipt`)

The physical arrival of returned goods, identified by `receiptId`; opened
against an RMA (whose state moves AUTHORIZED -> RECEIVED by the use case) or,
for a parcel with no RMA at all, opened with unexpected lines only.

Invariants:

1. **Every line references an RMA line or is UNEXPECTED — the constructor
   refuses a third state.** A line is built either as a matched line
   (carrying a non-blank `rmaId`) or as an unexpected one; a line that is
   neither (no kind, or a kind with a blank/absent counterpart) is refused,
   so no silent middle state can exist.
2. **Inspected qty <= received qty.** An inspection of `n` units is refused
   when `n` is below 1 or above the line's received quantity.
3. **A line is inspected once.** A second inspection of the same line is
   refused with `ErrLineAlreadyInspected`.

Events: `ReturnReceiptOpened` (all lines), `UnexpectedReturnReceived` (one
per UNEXPECTED line), `ReturnLineInspected` (grade A/B/C/SCRAP + reason
codes). The disposition is decided by the use case in the same transaction.

### Disposition (package `disposition`)

The immutable decision for one inspected line; identified by
`dispositionId`; carries the outcome, the grade, the line and the
`policy_version` it used. Outcome = `f(grade, classification, policy
version)`.

Invariants:

1. **`Decide` is deterministic and total over (grade x classification x
   policy).** It is a pure function: no I/O, no clock, no randomness. The
   policy table must carry a `productClass: "*"` default for every grade
   (validation refuses a policy without one), so every combination answers.
2. **RESTOCK only for grade A/B, and never for a DISPOSE-classified SKU or a
   hazmat SKU without a DOT rating.** A RESTOCK the table returns under any
   of those conditions is demoted to the policy's `vetoOutcome` (default
   DISPOSE) — fail-closed, per ADR 0001's assumptions.
3. **An override creates a NEW Disposition carrying `supersedes` and a
   non-empty reason.** The original is never mutated; a blank reason is
   refused. The override keeps the original's `policy_version` (the version
   under discussion).
4. **`ReturnLineStaged` is emitted once per RESTOCK disposition (idempotent
   on disposition id).** Creation of a RESTOCK disposition raises it; a
   repeated staging call is refused (`ErrAlreadyStaged`) and a non-RESTOCK
   disposition cannot stage at all (`ErrNotRestock`). No other outcome
   touches any sibling.

Events: `DispositionDecided`, `ReturnLineStaged`.

### DispositionPolicy is data, not code

The policy is a versioned operator table, append-only, declared through REST
(`PUT /disposition-policies/{version}`) and consumed by the pure `Decide`.
Why data and not a strategy interface: the fleet rule (ADR 0001) names the
policy object replaceable, and the safe way to replace a decision table
people argue about is a new **version** of a row set, not a new code path —
every decision records the version that made it, an override explains itself
against that version, and a bad table is rolled back by declaring a new
version, never by redeploying. The only logic in code is the guardrails
invariant 2 fixes (grade ceiling, forced outcome, hazmat-without-rating),
which are safety rules, not operator tuning.

Shape:

```
{version >= 1, return_window_days >= 0, max_units_per_line >= 1,
 vetoOutcome (default DISPOSE), rules: [{grade, productClass|"*", outcome}]}
```

`*` is the per-grade default; an exact `(grade, productClass)` entry wins
over it. `Classification` (from the product-master local copy) is the other
input: hazmat flag, DOT hazard class (0 = no rating), temperature class and
an optional forced outcome for a SKU that must never restock.

### shared (package `shared`)

Validated ids (`rma-`, `rcpt-`, `disp-` prefixes are the API's, the domain
only demands non-blank, no NUL, no ECMA-262 blank), `Quantity` (an integer
>= 1), `SiteID`, and `OrderLineRef` with the work-unit formula
`{order_id}-line-{line_no}` and its parser (the reverse of
fulfillment-execution's `order_ref`).

## Consequences

- Every decision an operator questions ("why DISPOSE", "why not restock") is
  answered by the disposition's own fields plus the policy version it names.
- Mutation testing covers every domain package (Makefile `mutation` target
  and the `mutation-fast` CI step loop over `returnauthorization`,
  `returnreceipt`, `disposition` and `shared`); thresholds are in
  `.gremlins.yaml`.
- The one-open-RMA rule is enforced twice on purpose: the aggregate refuses
  a known open sibling, and a partial unique index (wave 2) closes the race
  the aggregate cannot see.
- The state machine keeps REQUESTED even though v1 decides immediately in
  one transaction: it is the state an RMA is born in inside that
  transaction, and a future async decision path (carrier integration) would
  reuse it without a breaking change.
