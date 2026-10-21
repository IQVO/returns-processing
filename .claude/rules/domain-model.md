# Domain model: ubiquitous language, aggregates, events, use cases

## Ubiquitous Language (use these exact names)

- **ReturnAuthorization (RMA)** — permission to send a shipped order line back; one open RMA per (orderRef, lineNo); carries the authorized quantity and the policy window.
- **ReturnReceipt** — the physical arrival of returned goods; its lines each reference an RMA line or are explicitly `UNEXPECTED` (never a silent third state).
- **Grade** — inspection outcome of a return line: `A`, `B`, `C`, `SCRAP`, with reason codes.
- **Disposition** — the immutable decision for one inspected line: `RESTOCK`, `REFURBISH`, `LIQUIDATE`, `DISPOSE`, `RTV`; an override creates a superseding Disposition.
- **DispositionPolicy** — versioned operator table (grade x product class -> outcome, return window days, max units per line).
- **Staged line** — a `RESTOCK` handed to inventory-storage (`ReturnLineStaged`); the stow stays an RF action there.
- **Unexpected return** — a received line with no matching RMA; it opens a problem-solve case downstream.

## Aggregates

- **ReturnAuthorization** (`internal/domain/returnauthorization`): status machine `REQUESTED -> AUTHORIZED | REJECTED -> RECEIVED -> CLOSED`, no transition out of a terminal state; authorizable only for a manifested work unit `{order_id}-line-{line_no}` inside the window; quantity <= policy max.
- **ReturnReceipt** (`internal/domain/returnreceipt`): every line references an RMA line or is `UNEXPECTED`; inspected qty <= received qty; a line is inspected exactly once.
- **Disposition** (`internal/domain/disposition`, the richest aggregate and the mutation-gate package): outcome = f(grade, classification, policy version), recorded with the version used; `RESTOCK` only for grade A/B and never for a `DISPOSE`-classified SKU; `ReturnLineStaged` raised once per disposition; immutable, superseded by override.
- **DispositionPolicy** (`internal/domain/disposition`): append-only versions.

Read models (projections, never aggregates): allocated order lines, manifested work units, product classifications.

## Domain events

- `ReturnAuthorized` / `ReturnAuthorizationRejected` — RMA decided.
- `ReturnReceiptOpened` — arrival matched to an RMA (or opened with unexpected lines).
- `ReturnLineInspected` — grade and reason codes recorded.
- `DispositionDecided` — policy outcome for a line.
- `ReturnLineStaged` — RESTOCK handed to inventory-storage.
- `UnexpectedReturnReceived` — line with no RMA.

## Key use cases (`internal/application/usecases`)

- `AuthorizeReturn` / `RejectReturn` — RMA against the local copies of allocated lines + manifested work units; `ReturnAuthorized` or `ReturnAuthorizationRejected`.
- `OpenReturnReceipt` — opens a receipt against an RMA; `UnexpectedReturnReceived` for unmatched lines.
- `InspectReturnLine` — records the grade and runs `DecideDisposition` in the same transaction.
- `DecideDisposition` / `OverrideDisposition` — policy outcome; override needs a reason.
- `DeclareDispositionPolicy` — new policy version.
- `GetReturn`, `ListReturnsByOrder`, `GetDispositionPolicy` — reads (REST + read-only MCP).
