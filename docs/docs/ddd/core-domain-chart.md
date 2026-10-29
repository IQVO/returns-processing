---
id: core-domain-chart
title: Core domain chart
sidebar_label: Core domain chart
sidebar_position: 10
description: Where returns-processing sits in the warehouse-systems core/supporting/generic split today — Supporting, because the differentiator is the ledger it feeds.
---

# Core domain chart

The ddd-crew Core Domain Chart, drawn for the contexts returns-processing
touches. Colour says classification today ([ADR 0001](/docs/adr/0001-returns-processing-bounded-context)
fixes this context's own; the siblings' classifications are each one's own
ADR 0001, tallied on warehouse-docs' subdomain-classification page).

```mermaid
flowchart LR
    subgraph CORE["Core - what the warehouse wins or loses on"]
        IS["inventory-storage"]
        FE["fulfillment-execution"]
    end
    subgraph SUP["Supporting - necessary, replaceable policy objects"]
        RP["returns-processing"]
        PM["product-master"]
        EM["exception-management<br/>(candidate Core)"]
    end
    subgraph GEN["Generic / Supporting border"]
        OM["order-management"]
    end
    OM -- "allocated lines (facts)" --> RP
    FE -- "manifested work units (facts)" --> RP
    PM -- "classifications (facts)" --> RP
    RP -- "ReturnLineStaged feeds the ledger" --> IS
    RP -- "UnexpectedReturnReceived" --> EM
```

## Why supporting today

- Value recovery is material — returns run 20–30 % of e-commerce volume —
  but the differentiator is **the ledger it feeds**, not the returns logic
  itself (ADR 0001): a `RESTOCK` reuses inventory-storage's proven
  staged-receipt → stow seam unchanged.
- The disposition policy is **a replaceable policy object**: a versioned
  operator table, data and not code (ADR 0002). A bad table is rolled back
  by declaring a new version, never by redeploying.
- Four of the five outcomes (REFURBISH, LIQUIDATE, DISPOSE, RTV) are
  recorded for audit and touch no sibling; only RESTOCK has a downstream
  effect, and it asks inventory-storage to do the work.
- Every input is another context's fact (an allocated line, a manifested
  work unit, a classification) replicated into local copies (ADR 0003);
  delete the RMA, receipt and disposition rows and the forward flow keeps
  working — the facts keep arriving and the record rebuilds.

## What flips it to core

ADR 0001 declares no such condition and none is claimed here. The two
roadmap items are additive producer changes that complete the current
shape, not reclassify it:

1. order-management adding `quantity` to its released lines, which turns
   the shipped-quantity check from policy cap into a real check (ADR 0003);
2. inventory-storage promoting `ItemStowed` to its integration topic,
   which closes the restock loop this context deliberately leaves open
   (ADR 0001/0003).

Until then: invest for supporting — simple, fixed, well-tested; do not
gold-plate.
