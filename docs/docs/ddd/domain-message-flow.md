---
id: domain-message-flow
title: Domain message flow
sidebar_label: Domain message flow
sidebar_position: 15
description: A fact's journey from a producer's topic through the local copies and use cases to a disposition, and how every decision leaves through the outbox and its analytics twin.
---

# Domain message flow

One page, end to end: how another context's fact becomes an authorized
return, a graded line and an immutable decision. Every box below is a real
package in this repo.

```mermaid
flowchart TB
    subgraph PRODUCERS["Producer topics (Kafka, CloudEvents 1.0)"]
        T1["warehouse.product-master.events"]
        T2["warehouse.order-management.events"]
        T3["warehouse.fulfillment.events"]
    end
    subgraph INBOUND["internal/adapters/inbound/kafka"]
        C1["product-classes consumer"]
        C2["allocated-lines consumer"]
        C3["manifests consumer"]
    end
    subgraph APP["internal/application/usecases"]
        CP["ApplyProductClassification /<br/>ApplyAllocations / ApplyManifest"]
        AU["AuthorizeReturn"]
        OR["OpenReturnReceipt"]
        IL["InspectReturnLine"]
        OV["OverrideDisposition"]
        DP["DeclareDispositionPolicy"]
    end
    subgraph DOM["internal/domain"]
        RMA["returnauthorization.RMA"]
        RCP["returnreceipt.Receipt"]
        DIS["disposition.Disposition +<br/>DispositionPolicy (Decide)"]
    end
    subgraph OUT["Outbound (same transaction)"]
        PG[("Postgres OLTP<br/>aggregates, local copies,<br/>processed_events, outbox,<br/>idempotency_keys")]
        OB["outbox rows"]
    end
    RELAY["outbox relay<br/>(OUTBOX_RELAY_INTERVAL, attempt cap)"]
    EVT["warehouse.returns-processing.events"]
    ANA["warehouse.returns-processing.analytics"]
    DLQ["warehouse.returns-processing.events.dlq"]
    PRJ["cmd/returns-projector"]
    ADB[("analytics Postgres<br/>disposition_facts,<br/>return_line_facts")]
    RPT["cmd/returns-reports :8092"]

    T1 --> C1 --> CP
    T2 --> C2 --> CP
    T3 --> C3 --> CP
    CP -- "upsert the copy" --> PG

    REST["REST commands + reads"] --> AU
    REST --> OR
    REST --> IL
    REST --> OV
    REST --> DP
    AU --> RMA
    OR --> RCP
    IL --> RCP
    IL -- "grade + classification + policy" --> DIS
    OV --> DIS
    DP --> DIS
    RMA -- "Save + events" --> PG
    RCP -- "Save + events" --> PG
    DIS -- "Save + events" --> PG
    RMA -- domain facts --> OB
    RCP -- domain facts --> OB
    DIS -- domain facts --> OB

    OB --> RELAY
    RELAY -- "one row, two messages" --> EVT
    RELAY -- "twin, same id" --> ANA
    RELAY -- "poison rows" --> DLQ
    ANA --> PRJ --> ADB --> RPT
    MCP["MCP tools (read-only)"] --> RD["read use cases"]
```

The flow in words:

1. **A fact lands** on one of the three consumed topics; the consumer under
   its pinned group id decodes the CloudEvents envelope
   (`internal/adapters/kafka/cloudevents`) and dispatches on the FULL type;
   unknown types are ignored; a non-CloudEvent message is WARN + skip.
2. **Id claim first.** The CloudEvents `id` is claimed in the SAME Postgres
   transaction as the copy write (`processed_events`); a replayed id is a
   no-op that still commits the Kafka offset (`FetchMessage` +
   `CommitMessages` — offset only after a durable effect).
3. **The copies answer the decision.** `AuthorizeReturn` reads only the
   local copies: the allocations copy supplies the authoritative SKU, the
   manifested copy proves the line shipped, the current policy supplies the
   window and the cap. An empty or stale copy rejects
   (`LINE_NOT_MANIFESTED`), never a guess and never a live call (ADR 0003).
4. **A refusal persists nothing.** A request that fails an invariant returns
   the typed domain error (`ErrLineNotManifested`, `ErrWindowExpired`,
   `ErrOpenRMAExists`, `ErrQuantityExceedsPolicy`) mapped to the rejection
   code — no RMA row, no event. The domain's `Reject` path and the
   catalogued `ReturnAuthorizationRejected` exist for a future async
   decision path (ADR 0002 keeps `REQUESTED` for it).
5. **Arrival and grading.** `OpenReturnReceipt` records every line as
   MATCHED or explicitly UNEXPECTED (one `UnexpectedReturnReceived` per
   unexpected line, which exception-management opens a case on).
   `InspectReturnLine` grades a line exactly once and decides its
   disposition in the same unit of work: the pure `Decide(grade,
   classification, policy)` plus the RESTOCK guardrails.
6. **Everything leaves through the outbox** ([ADR 0005](/docs/adr/0005-transactional-outbox-and-relay)):
   the relay drains due rows, encodes to CloudEvents, deletes on publish,
   backs off exponentially, caps attempts (`OUTBOX_MAX_ATTEMPTS`) and
   dead-letters deterministic poison. Each drained row produces TWO
   messages — the integration event and its analytics twin (same `id`, only
   the dataschema differs, [ADR 0007](/docs/adr/0007-analytics-read-side)).
7. **The analytics twin feeds the read side**: `cmd/returns-projector`
   folds five of the seven event types into fact tables;
   `cmd/returns-reports` answers disposition-mix, value-recovery and
   return-cycle-time over them.
