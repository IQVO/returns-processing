---
id: business-context
title: Business context
sidebar_label: Business context
sidebar_position: 2
---

# Business context

Why a warehouse needs a returns context at all, what it costs when there is none, and what this
service changes. The modelling that follows from here is on the
[Bounded context canvas](/docs/ddd/bounded-context-canvas).

## The problem: goods coming back had no owner

Before this context existed, no service in the fleet owned the reverse flow. Every forward flow has
an owner — `inbound-receiving` owns supplier arrivals, `order-management` owns the promise,
`fulfillment-execution` owns the shipment — but a returned parcel was nobody's:

- **No authorization.** Anything could be claimed a return; there was no check that the line was
  ever shipped, no return window, no cap on quantity.
- **No receiving.** A return was hand-keyed as a supplier receipt — wrong provenance, no audit
  trail — or lost at the dock.
- **No grading, no decision.** Nothing recorded *what condition* the goods were in or *what happens
  next*: restock, refurbish, liquidate, dispose or return to vendor. Restockable stock went to
  landfill with the scrap; scrap quietly re-entered sellable inventory.

Returns are not a receiving variant: they are a **decision-driven flow**. The decision depends on an
inspection grade, the product's classification and a policy, and only ONE of the five outcomes
touches the stock ledger. That lifecycle and its policy object belong to a context of their own
([ADR 0001](/docs/adr/0001-returns-processing-bounded-context)).

## What it is worth

- Returns run **20–30 % of e-commerce volume**; every mishandled line is either lost resale value
  (restockable goods scrapped) or sellable-inventory contamination (scrap restocked).
- The differentiator is not the returns logic itself — this is a **Supporting subdomain**: valuable,
  necessary, but a commodity shape. The value is keeping the **ledger it feeds** accurate, which is
  why a `RESTOCK` reuses inventory-storage's proven staged-receipt → stow seam instead of writing
  stock directly.
- The **DispositionPolicy is an operator table, not code**: merchandising tunes the grade × product
  class → outcome mapping per version, every decision records the version that made it, and a bad
  table is rolled back by declaring a new version — never by redeploying.

## The flow, business-first

1. **A customer wants to send something back.** The service answers only for a line that was both
   allocated and *shipped* (the manifest is the shipped fact), inside the policy window, with
   quantity within the policy cap. One open RMA per line, ever.
2. **The parcel arrives.** Each line is matched to its RMA — or received as explicitly UNEXPECTED,
   which immediately raises a case for problem-solving (exception-management). There is no silent
   third state: unmatched is a first-class, auditable fact.
3. **An operator grades the line** (A, B, C, SCRAP, with reason codes). Grading happens exactly
   once per line.
4. **The policy decides.** Grade + product classification (hazmat, temperature, DOT class from
   product-master) + the current policy version → one of RESTOCK, REFURBISH, LIQUIDATE, DISPOSE,
   RTV. Guardrails are hard-coded safety, not tuning: RESTOCK only for grade A/B, never for a
   DISPOSE-classified SKU, never for a hazmat SKU without a DOT rating — demoted to the veto
   outcome, fail-closed.
5. **Only RESTOCK touches the ledger**: the line is staged into inventory-storage
   (`ReturnLineStaged`, the same shape as a supplier receipt line) and the stow stays an RF action
   there. REFURBISH, LIQUIDATE, DISPOSE and RTV are recorded for audit and touch no sibling.

## What it deliberately does not do (v1)

| Excluded | Why |
| --- | --- |
| Carrier label / pickup booking | external party; would need an ACL — separate decision |
| Refund or credit issuing | finance, not warehouse |
| Refurbishment work orders | if ever, a fulfillment-execution task type, not this context |
| Writing `StockUnit` or `Bin` | the ledger stays in inventory-storage |
| Any live REST/MCP call to a sibling | fleet rule; authorization reads local copies only |

## Roadmap (planned, not built — never faked)

- **Shipped-quantity check per line**: order-management's allocated lines carry no `quantity`; until
  that additive producer change lands, the authorized quantity is capped by
  `DispositionPolicy.max_units_per_line` only.
- **Restock loop closure**: inventory-storage's `ItemStowed` is not yet on its integration topic;
  when promoted, this service can consume it to see a staged line reach a bin.

Built since this page last changed: the **analytics read side** (ADR 0007 —
`warehouse.returns-processing.analytics`, projector, reports) shipped on
2026-10-21; see [Analytics](/docs/operations/analytics).

Source: [ADR 0001](/docs/adr/0001-returns-processing-bounded-context), the fleet plan
(`.hermes/plans/2026-10-20_231017-returns-processing-and-exception-management.md`).
