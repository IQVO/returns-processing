---
id: sequence-diagrams
title: Sequence diagrams
sidebar_label: Sequence diagrams
sidebar_position: 19
description: The end-to-end flows as sequence diagrams — the local copies, authorization, receiving, inspect-and-decide, the override, the outbox relay and the analytics projector.
---

# Sequence diagrams

## A producer fact refreshes a local copy

```mermaid
sequenceDiagram
    autonumber
    participant K as Kafka (producer topic)
    participant C as one of the 3 consumers
    participant UC as Apply* (consume_*)
    participant PG as Postgres

    K->>C: ProductClassified / OrderAllocated / PackageManifested
    C->>UC: decode, dispatch on full type
    UC->>PG: BEGIN claim CloudEvents id (processed_events)
    alt id already claimed
        UC->>C: no-op, still commit the offset
    else first sight
        UC->>PG: upsert the copy row (same tx)
        UC->>C: commit Kafka offset
    end
    Note over C,UC: unknown types ignored, a non-CloudEvent is WARN + skip
```

Version rules per copy (ADR 0003): a classification applies only when its
`version` is greater than the stored one; an allocation upserts per
(order_id, line_no) — a partial allocation refreshes only its own lines; a
manifest inserts per work unit `{order_id}-line-{line_no}` and is idempotent
on that key.

## Authorize a return

```mermaid
sequenceDiagram
    autonumber
    participant O as Operator or console
    participant H as REST (chi + Idempotency-Key)
    participant UC as AuthorizeReturn
    participant AG as returnauthorization.RMA
    participant PG as Postgres

    O->>H: POST /returns {orderId, lineNo, quantity, siteId, reasonCode}
    H->>H: Idempotency-Key middleware (replay = stored answer)
    H->>UC: the request (no SKU - the copy is the truth)
    UC->>PG: current policy, allocated line, manifest, open-RMA query
    alt line not in the allocations copy
        UC-->>O: 404 order-line-not-allocated
    else an invariant fails
        UC->>AG: Request + Authorize(facts) refuses
        AG-->>UC: typed error (ErrLineNotManifested, ErrWindowExpired, ...)
        UC-->>O: 4xx with the rejection code - nothing persists
    else decided
        UC->>AG: Request + Authorize(facts)
        AG-->>UC: ReturnAuthorized (version 1)
        UC->>PG: INSERT return_authorizations + outbox (one tx)
        UC-->>O: 201, the RMA
    end
```

A refused request persists nothing and publishes nothing: the rejection code
is mapped from the typed domain error at the HTTP edge
(`RejectionCodeFor`). The partial unique index
`uq_rmas_one_open_per_order_line` decides a race the aggregate cannot see.

## A parcel arrives (matched and unexpected lines)

```mermaid
sequenceDiagram
    autonumber
    participant O as Operator
    participant H as REST
    participant UC as OpenReturnReceipt
    participant AG as returnreceipt.Receipt
    participant PG as Postgres
    participant EM as exception-management

    O->>H: POST /returns/{rmaId}/receipts {siteId, lines[]}
    H->>UC: each line matched (rma id) or unexpected (none)
    UC->>AG: Open(OpenInput) - MatchedLine / UnexpectedLine only
    AG-->>UC: ReturnReceiptOpened + one UnexpectedReturnReceived per unexpected line
    UC->>PG: INSERT return_receipts + lines + outbox (one tx)
    Note over EM: the relay publishes, exception-management opens a case per UnexpectedReturnReceived
```

A line that is neither matched nor unexpected (no kind, or a kind with a
blank/absent counterpart) is refused by the constructor — there is no
silent third state. v1 does not move the RMA to RECEIVED here: the receipt
carries the arrival fact.

## Inspect, decide, stage

```mermaid
sequenceDiagram
    autonumber
    participant O as Operator
    participant UC as InspectReturnLine
    participant R as Receipt
    participant D as disposition (Decide)
    participant PG as Postgres
    participant IS as inventory-storage

    O->>UC: POST /receipts/{id}/lines/{n}/inspect {grade, quantity, reasonCodes}
    UC->>R: load receipt, Inspect(line) - once, qty within received
    UC->>UC: classification from the product_classes copy
    UC->>D: DecideForLine(grade, classification, current policy)
    D-->>UC: DispositionDecided (+ ReturnLineStaged when RESTOCK)
    UC->>PG: UPDATE receipt + INSERT disposition + outbox (one tx)
    Note over IS: ReturnLineStaged is the handover, the stow stays an RF action there
```

The guardrails run inside `Decide`: a table RESTOCK for grade C/SCRAP or a
hazmat SKU without a DOT rating comes back as the policy's `vetoOutcome` —
fail-closed, and the recorded `policy_version` explains every decision.

## An override supersedes

```mermaid
sequenceDiagram
    autonumber
    participant O as Operator
    participant UC as OverrideDisposition
    participant D as Disposition
    participant PG as Postgres

    O->>UC: POST /dispositions/{id}/override {outcome, reason}
    UC->>D: load the original
    UC->>D: Override(new id, outcome, reason)
    D-->>UC: a NEW Disposition: supersedes + reason, original's policy_version
    alt the override outcome is RESTOCK
        UC->>D: Stage() - raises ReturnLineStaged once
    end
    UC->>PG: INSERT superseding disposition + outbox (one tx)
    Note over D: the original is never mutated, a blank reason is refused
```

## Everything leaves through the outbox relay

```mermaid
sequenceDiagram
    autonumber
    participant R as relay (OUTBOX_RELAY_INTERVAL, default 1s)
    participant PG as Postgres
    participant K as Kafka
    participant D as warehouse.returns-processing.events.dlq

    R->>PG: claim up to 100 due rows FOR UPDATE SKIP LOCKED
    loop each row in id order
        alt send ok
            R->>K: publish CloudEvent on events + the analytics twin (same id)
            R->>PG: DELETE row (same tx - removal is the sent signal)
        else send error
            R->>PG: attempts + 1, next_attempt_at pushed back (cap 5 min)
            Note over R: the pass stops, later rows stay for the next one
        else no wire shape (poison)
            R->>D: publish row raw, delete row
        end
    end
```

A row reaching `OUTBOX_MAX_ATTEMPTS` (default 20) is excluded from the
claim, logged once at ERROR and stays in the outbox for inspection;
re-arming is a runbook operation
([Runbook](/docs/operations/runbook)). Delivery is at-least-once with a
stable id per occurrence — consumers dedupe on the CloudEvents `id`
([ADR 0005](/docs/adr/0005-transactional-outbox-and-relay)).

## The analytics twin becomes a report

```mermaid
sequenceDiagram
    autonumber
    participant A as Kafka (returns-processing.analytics)
    participant P as cmd/returns-projector
    participant DB as analytics Postgres
    participant RP as cmd/returns-reports

    A->>P: twin event (ANALYTICS_CONSUMER_GROUP)
    P->>DB: claim id (analytics_processed_events) + fold the fact (one tx)
    Note over P,DB: milestones keep the EARLIEST time seen - order never matters
    RP->>DB: GET /reports/return-cycle-time?from=...&to=...
    DB-->>RP: counted from return_line_facts over the window
```

Five of the seven event types fold into the model (`ReturnReceiptOpened`,
`UnexpectedReturnReceived`, `ReturnLineInspected`, `DispositionDecided`,
`ReturnLineStaged`); the two authorization twins are ignored by the
projector. Deterministic poison is dead-lettered to
`warehouse.returns-processing.analytics.dlq`
([ADR 0007](/docs/adr/0007-analytics-read-side)).
