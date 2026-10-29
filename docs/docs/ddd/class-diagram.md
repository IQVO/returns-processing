---
id: class-diagram
title: Class diagram
sidebar_label: Class diagram
sidebar_position: 17
description: The domain packages' types and relationships — returnauthorization, returnreceipt, disposition, shared — exactly as they exist in internal/domain.
---

# Class diagram

The domain model exactly as it exists in `internal/domain` (field and
method names taken from the code; pure Go, no I/O).

```mermaid
classDiagram
    class RMA {
        +ID id
        +OrderLineRef line
        +ID sku
        +Quantity quantity
        +SiteID site
        +string reasonCode
        +Status status
        +Time windowEndsAt
        +Time decidedAt
        +RejectionCode rejectionCode
        +Time receivedAt
        +Time closedAt
        +int64 version
        +[]Event events
        +Authorize(f Facts)
        +Reject(code, at)
        +MarkReceived(at)
        +Close(at)
        +Open() bool
        +DomainEvents() []Event
        +Snapshot() Snapshot
    }
    class RequestInput {
        +string RMAID
        +string OrderID
        +int32 LineNo
        +string SKU
        +int64 Quantity
        +string SiteID
        +string ReasonCode
    }
    class Facts {
        +string ExistingOpenRMAID
        +string ManifestedWorkUnit
        +int64 PolicyMaxUnits
        +Time WindowEndsAt
        +Time Now
    }
    class Receipt {
        +ID id
        +ID rmaID
        +SiteID site
        +[]Line lines
        +Time openedAt
        +int64 version
        +[]Event events
        +Inspect(lineNo, grade, qty, reasons, at)
        +Line(lineNo) Line
        +DomainEvents() []Event
        +Snapshot() Snapshot
    }
    class Line {
        +int32 lineNo
        +ID sku
        +Quantity quantity
        +LineKind kind
        +ID rmaID
        +bool inspected
        +Quantity inspectedQty
        +Grade grade
        +[]string reasons
        +Time inspectedAt
    }
    class OpenInput {
        +string ReceiptID
        +string RMAID
        +string SiteID
        +[]Line Lines
        +Time At
    }
    class Disposition {
        +ID id
        +ID receiptID
        +int32 lineNo
        +ID sku
        +Quantity quantity
        +Grade grade
        +Outcome outcome
        +int64 policyVersion
        +ID supersedes
        +string reason
        +SiteID site
        +ID rmaID
        +Time receivedAt
        +Time decidedAt
        +bool staged
        +int64 version
        +[]Event events
        +Override(in OverrideInput) Disposition
        +Stage()
        +DomainEvents() []Event
        +Snapshot() Snapshot
    }
    class LineDecision {
        +string DispositionID
        +string ReceiptID
        +int32 LineNo
        +string SKU
        +int64 Quantity
        +Grade Grade
        +Classification Classification
        +DispositionPolicy Policy
        +string SiteID
        +string RMAID
        +Time ReceivedAt
        +Time DecidedAt
    }
    class Classification {
        +string SKU
        +string ProductClass
        +bool Hazmat
        +int DoTHazardClass
        +string TemperatureClass
        +Outcome Forces
        +bool Classified
    }
    class DispositionPolicy {
        +int64 version
        +int32 returnWindowDays
        +int64 maxUnitsPerLine
        +Outcome vetoOutcome
        +[]Rule rules
        +Time declaredAt
        +WindowEndsFrom(t) Time
        +Rules() []Rule
    }
    class Rule {
        +Grade Grade
        +string ProductClass
        +Outcome Outcome
    }

    RequestInput ..> RMA : Request() builds one
    Facts ..> RMA : Authorize decides with copy facts
    OpenInput ..> Receipt : Open() builds one
    Receipt *-- Line : the parcel's lines
    LineDecision ..> Disposition : DecideForLine() builds one
    Disposition ..> Disposition : Override creates the superseding one
    DispositionPolicy *-- Rule : the operator table
    Disposition ..> Classification : one Decide input
```

Facts worth knowing that the diagram cannot show:

- `Request` and `Open` are **package functions** of `returnauthorization`
  and `returnreceipt` (they validate the input and build the aggregate);
  `DecideForLine`, `Decide` and `Declare` are package functions of
  `disposition`. Everything after creation is a method on the aggregate —
  except `Decide`, which is a pure function over (grade, classification,
  policy) with no receiver.
- `Status` is a string type: `REQUESTED | AUTHORIZED | REJECTED | RECEIVED
  | CLOSED`, with `REJECTED` and `CLOSED` terminal. v1 decides in one
  transaction (REQUESTED → AUTHORIZED inside `AuthorizeReturn`); `Reject`,
  `MarkReceived` and `Close` are implemented and repository-tested but have
  no v1 use case caller.
- `RejectionCode` is a string type: `LINE_NOT_MANIFESTED`,
  `RETURN_WINDOW_EXPIRED`, `OPEN_RMA_EXISTS`, `QUANTITY_EXCEEDS_POLICY`;
  `RejectionCodeFor` maps the typed domain errors to the codes at the HTTP
  edge.
- A `Line` is built by `MatchedLine` or `UnexpectedLine` only, so a line is
  always exactly one kind — the zero `Line` (the refused third state) fails
  `Open`. `LineKind` is `MATCHED | UNEXPECTED`.
- `Decide`'s order of precedence: a forced outcome on the classification
  wins outright; then the table's exact (grade, class) entry or its `*`
  default; then a RESTOCK the table returned is demoted to `vetoOutcome`
  when the grade is below B or the SKU is hazmat without a DOT rating.
- The domain events (`ReturnAuthorized` … in each aggregate's `events.go`)
  are plain structs with a per-package `Header` and `OccurredAt()`; they
  know nothing of CloudEvents or Kafka — the outbound adapter maps them
  (ADR 0004).
- Repositories persist and restore through the immutable `Snapshot` (each
  aggregate's `snapshot.go`; `Restore` validates, so a corrupt row can
  never become a live aggregate); the views DTO
  (`internal/application/views`) is what inbound adapters see.
- `shared.ID` (validated, non-blank, no NUL), `shared.Quantity` (an
  integer ≥ 1), `shared.SiteID`, `shared.Grade` (`A | B | C | SCRAP`) and
  `shared.OrderLineRef` with the work-unit formula `{order_id}-line-{line_no}`
  (`WorkUnit()`, the reverse of fulfillment-execution's `order_ref`) are the
  shared value objects.
