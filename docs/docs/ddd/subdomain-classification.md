---
id: subdomain-classification
title: Subdomain classification
sidebar_label: Subdomain classification
sidebar_position: 2
description: "Returns Processing is a Supporting subdomain in the wms tier of the warehouse-systems fleet: why it is neither Core nor Generic, what would move it, and its neighbours' verdicts."
---

# Subdomain classification

`returns-processing` is a **Supporting** bounded context in the warehouse-management (`wms`)
tier: every event type it publishes starts `com.warehouse.wms.returns-processing.`. The verdict is
the one fixed by [ADR 0001](/docs/adr/0001-returns-processing-bounded-context) (Strategic
classification: *Supporting subdomain*), listed in the portfolio scorecard and in the aggregator's
[Subdomain classification](https://iqvo.github.io/warehouse-docs/strategic-design/subdomain-classification)
(`docs/strategic-design/subdomain-classification.md` on `origin/main`): business model *value
recovery / compliance* (accurate return record and disposition audit), evolution *custom-built
(genesis)*. The neighbour rows below are copied from that table, not recalled. The pages were read
on 2026-11-01; if the aggregator has moved since, it wins.

## Why Supporting

- **Not Core.** Value recovery is material (returns run 20-30 % of e-commerce volume), but the
  differentiator is **the ledger it feeds**, not the returns logic itself. A `RESTOCK` reuses
  `inventory-storage`'s staged-receipt and stow seam unchanged: this context only raises
  `ReturnLineStaged` and asks that sibling to do the work. Four of the five outcomes
  (`REFURBISH`, `LIQUIDATE`, `DISPOSE`, `RTV`) are recorded for audit and touch no stock. The
  decision itself is a pure function of the inspection grade (`A`, `B`, `C`, `SCRAP`), the SKU's
  classification and a policy version, and the policy is a **replaceable policy object**: a
  versioned operator table, data and not code. A bad table is rolled back by declaring a new
  version, never by redeploying ([ADR 0002](/docs/adr/0002-aggregates-and-policy)).
- **Not Generic.** The rules are the fleet's own and not bought unchanged: a return is authorizable
  only against a shipped line (a manifested work unit `{order_id}-line-{line_no}`) inside the policy
  window, one open RMA per order line, a receipt line references an RMA line or is explicitly
  unexpected, a line is inspected once, and a disposition is immutable (an override creates a
  superseding one with a reason). The grade-by-product-class table, the hazmat guardrail and the
  `RESTOCK` handover depend on the fleet's classification tags and its inventory seam, so an
  off-the-shelf returns product would need an anti-corruption layer on both sides.
- **Every input is a sibling's fact.** The allocated lines, the manifested work units and the
  classifications are replicated into local copies from `order-management`,
  `fulfillment-execution` and `product-master`
  ([ADR 0003](/docs/adr/0003-local-copies-and-consumed-contracts)), see [Integration](/docs/ecosystem/integration).
  Delete the RMA, receipt and disposition rows and the forward flow keeps working, because the facts
  keep arriving and the record rebuilds.

Model complexity is low to moderate: three small aggregates (`RMA`, `Receipt`, `Disposition`) plus
the versioned `DispositionPolicy`, a pure decision function, read models fed by events, and no
algorithm that ranks or optimises. See the [Core domain chart](/docs/ddd/core-domain-chart) and the
[Aggregate design canvas](/docs/ddd/aggregate-design-canvas).

### Inside the context

The context-level verdict is the one that drives fleet decisions. Reading the code, the spread of
design investment inside it is uneven:

| Slice | Where | Reading |
| --- | --- | --- |
| Authorization rules and the disposition decision | `internal/domain/{returnauthorization,returnreceipt,disposition}` | Supporting: the part that is specific to this fleet and deserves the tests; kept small and fixed. |
| Local copies and CloudEvents consumers | `internal/adapters/inbound/kafka`, `internal/application/usecases/consume_*.go` | Supporting plumbing that follows the fleet pattern of ADR 0003. |
| Outbox, relay, CloudEvents envelope, idempotency keys | `internal/adapters/outbound/{postgres,kafka}`, `internal/adapters/kafka/cloudevents` | Generic: the envelope package is generated from the fleet's harness template and the rest repeats the fleet's outbox pattern. |
| Analytics read side | `cmd/returns-projector`, `cmd/returns-reports`, `internal/analytics` | Generic reporting over the event stream ([ADR 0007](/docs/adr/0007-analytics-read-side)). |

Source: the table is a reading of the code layout, not a verdict recorded in an ADR or in the
aggregator.

## What would move it

ADR 0001 declares no condition that flips the verdict and none is claimed here. The two roadmap
items complete the current shape instead of reclassifying it: `order-management` adding
`quantity` to its allocated lines (which turns the shipped-quantity check from a policy cap into
a real check), and `inventory-storage` promoting `ItemStowed` to its integration topic (which
closes the restock loop this context deliberately leaves open. Until then: invest
for Supporting, keep it simple, fixed and well tested, and do not gold-plate.

## Neighbours (from the aggregator's table)

| Bounded context | Classification | CloudEvents subdomain | Relation to this context |
| --- | --- | --- | --- |
| `product-master` | Supporting | `wms` | upstream (`ProductClassified`, conformist local copy) |
| `order-management` | Generic/Supporting | `wes` | upstream (`OrderAllocated`, `OrderPartiallyAllocated`) |
| `fulfillment-execution` | Core | `wes` | upstream (`PackageManifested`) |
| `inventory-storage` | Core | `wms` | downstream (consumes `ReturnLineStaged`) |
| `exception-management` | Supporting (candidate Core) | `wes` | downstream (consumes `UnexpectedReturnReceived`) |

Source: the classification and subdomain columns come from the aggregator's table; the relation
column comes from `internal/adapters/inbound/kafka/{product_classes,allocated_lines,manifests}.go`
and `internal/adapters/outbound/kafka/encoder.go`, verified on
[Integration](/docs/ecosystem/integration).

## Tier is not classification

The `wms` segment groups contexts by tier, not by Core, Supporting or Generic: `returns-processing`
and `product-master` share the tier and the Supporting class, while `inventory-storage` is `wms` and
Core, and `fulfillment-execution` is `wes` and Core. The tier decides the type prefix; the class
decides how much design investment the context deserves.
