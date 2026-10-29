---
id: entity-relationship
title: Entity-relationship
sidebar_label: Entity-relationship
sidebar_position: 18
description: The Postgres schemas behind the aggregates — OLTP migrations 0001 and 0002 plus the separate analytics database, every table, constraint and index, with the aggregate each belongs to.
---

# Entity-relationship

Two databases. The **OLTP schema** is two migrations
(`migrations/0001_returns-processing.up.sql`,
`migrations/0002_idempotency_keys.up.sql`), run embedded at boot by every
binary that touches it. The **analytics schema** is one migration
(`analytics/migrations/0001_returns_analytics.up.sql`) against a separate
database, written only by `cmd/returns-projector` and read only by
`cmd/returns-reports` ([ADR 0007](/docs/adr/0007-analytics-read-side)).

## OLTP

```mermaid
erDiagram
    return_authorizations {
        TEXT rma_id PK "rma-<uuid>, COLLATE C"
        TEXT order_id "COLLATE C"
        INTEGER line_no ">= 1"
        TEXT sku "COLLATE C"
        BIGINT quantity ">= 1"
        TEXT site_id
        TEXT reason_code "never blank"
        TEXT status "REQUESTED | AUTHORIZED | REJECTED | RECEIVED | CLOSED"
        TEXT rejection_code "'' or one of the four codes"
        TIMESTAMPTZ window_ends_at
        TIMESTAMPTZ decided_at
        TIMESTAMPTZ received_at
        TIMESTAMPTZ closed_at
        BIGINT version "0 while REQUESTED, 1 when decided, +1 per transition"
        TIMESTAMPTZ updated_at
    }
    return_receipts {
        TEXT receipt_id PK "rcpt-<uuid>, COLLATE C"
        TEXT rma_id "'' when the whole parcel was unexpected"
        TEXT site_id
        TIMESTAMPTZ opened_at
        BIGINT version "1 when opened, +1 per inspected line"
        TIMESTAMPTZ updated_at
    }
    return_receipt_lines {
        TEXT receipt_id PK "FK -> return_receipts, ON DELETE CASCADE"
        INTEGER line_no PK ">= 1"
        TEXT sku
        BIGINT quantity ">= 1"
        TEXT kind "MATCHED | UNEXPECTED"
        TEXT rma_id "MATCHED carries it, UNEXPECTED carries ''"
        BOOLEAN inspected
        BIGINT inspected_quantity ">= 0"
        TEXT grade "'' | A | B | C | SCRAP"
        JSONB reason_codes
        TIMESTAMPTZ inspected_at
    }
    dispositions {
        TEXT disposition_id PK "disp-<uuid>, COLLATE C"
        TEXT receipt_id
        INTEGER line_no ">= 1"
        TEXT sku
        BIGINT quantity ">= 1"
        TEXT grade "A | B | C | SCRAP"
        TEXT outcome "RESTOCK | REFURBISH | LIQUIDATE | DISPOSE | RTV"
        BIGINT policy_version ">= 1"
        TEXT supersedes "NULL on a policy decision"
        TEXT reason "mandatory on an override"
        TEXT site_id
        TEXT rma_id "'' when the line was UNEXPECTED"
        TIMESTAMPTZ received_at
        TIMESTAMPTZ decided_at
        BOOLEAN staged "ReturnLineStaged raised, once"
        BIGINT version "always 1 - decisions never transition"
        TIMESTAMPTZ updated_at
    }
    disposition_policies {
        BIGINT version PK "append-only, >= 1"
        INTEGER return_window_days ">= 0"
        BIGINT max_units_per_line ">= 1"
        TEXT veto_outcome "default DISPOSE"
        JSONB rules "[{grade, productClass|'*', outcome}]"
        TIMESTAMPTZ declared_at
        BOOLEAN is_current "at most one row true"
    }
    allocated_lines {
        TEXT order_id PK "COLLATE C"
        INTEGER line_no PK ">= 1"
        TEXT sku
        BIGINT version "local write counter; sources carry none"
        TIMESTAMPTZ updated_at
    }
    manifested_units {
        TEXT work_unit PK "{order_id}-line-{line_no}, COLLATE C"
        TEXT order_id
        INTEGER line_no ">= 1"
        TEXT package_id "UNIQUE"
        TIMESTAMPTZ manifested_at
        BIGINT version
        TIMESTAMPTZ updated_at
    }
    product_classes {
        TEXT sku PK "COLLATE C"
        JSONB tags "the handling tags (hazmat, DOT class, ...)"
        TEXT temperature_class
        BIGINT version "apply only when greater than stored"
        TIMESTAMPTZ updated_at
    }
    processed_events {
        TEXT event_id PK "CloudEvents id, claimed with the effect"
        TIMESTAMPTZ processed_at
    }
    outbox {
        BIGSERIAL id PK
        TEXT event_id UK "CloudEvents id, minted once at enqueue"
        TEXT aggreg_type
        TEXT aggreg_id "COLLATE C"
        JSONB payload "{type, data} - the domain fact"
        TIMESTAMPTZ occurred_at
        INTEGER attempts ">= 0"
        TIMESTAMPTZ next_attempt_at
        TIMESTAMPTZ created_at
    }
    idempotency_keys {
        TEXT key PK "the Idempotency-Key header, COLLATE C"
        TEXT fingerprint "hash of method, path and body"
        INTEGER status "only 2xx and 4xx answers stored"
        TEXT content_type
        BYTEA body "the stored answer"
        TIMESTAMPTZ created_at
    }

    return_receipts ||--|{ return_receipt_lines : "one parcel, its lines"
    return_authorizations ||--o{ return_receipts : "opened against (rma_id, '' when unexpected)"
    return_receipt_lines ||--o{ dispositions : "one policy decision, then overrides"
    disposition_policies ||--o{ dispositions : "policy_version the decision used"
```

The three local-copy tables (`allocated_lines`, `manifested_units`,
`product_classes`) belong to no aggregate: they are read models fed by the
consumers (ADR 0003), and `processed_events`, `outbox` and
`idempotency_keys` are the fleet machinery tables.

## The constraints that matter

| Constraint | Enforces |
| --- | --- |
| `uq_rmas_one_open_per_order_line` — UNIQUE `(order_id, line_no) WHERE status IN ('REQUESTED','AUTHORIZED','RECEIVED')` | Invariant 1 (ADR 0002): at most one open RMA per order line across instances; the loser of a race becomes the use case's `ErrOpenRMAExists` / `ErrConcurrentModification` mapping. |
| `CHECK ((kind = 'MATCHED') = (rma_id <> ''))` on `return_receipt_lines` | The refused third state cannot even be stored: a MATCHED line carries its RMA, an UNEXPECTED one carries none. |
| `uq_dispositions_one_policy_decision_per_line` — UNIQUE `(receipt_id, line_no) WHERE supersedes IS NULL` | At most one POLICY decision per line; an override (supersedes set) is outside the index, so a line can be superseded any number of times. |
| `CHECK (version = 1)` on `dispositions` | Decisions never transition: the immutable aggregate's whole optimistic-concurrency story. |
| `PRIMARY KEY (version)` + `uq_disposition_policies_one_current` | Append-only policy versions, at most one current; `Save` clears the previous current and upserts the new one in one transaction. |
| `event_id UNIQUE` in `outbox` | A relay retry republishes the same CloudEvents id byte-identically; consumers dedupe on it. |
| `CHECK (status >= 200 AND status < 500)` on `idempotency_keys` | Only 2xx and 4xx answers are replayable; a 5xx is never stored, so the caller retries for real. |

## Indexes

- `idx_rmas_by_order (order_id, line_no)` — `FindOpenByOrderLine` and
  `ListByOrder`.
- `idx_dispositions_by_line (receipt_id, line_no, decided_at DESC)` — the
  line's latest decision and the audit trail.
- `idx_outbox_due (attempts, next_attempt_at)` — the relay's claim scan
  (`FOR UPDATE SKIP LOCKED`, up to 100 due rows).
- `uq_manifested_units_package (package_id)` — the manifest copy's second
  key.
- `idx_idempotency_keys_created_at` — store cleanup.

Text keys compared or ordered in Go (ids, SKUs, order refs, work units) use
the `C` collation so `ORDER BY` follows byte order, the same order the
in-memory adapters use.

## Analytics (ADR 0007)

Facts, not aggregates: every report number is counted from these rows over
the requested window. Each milestone column keeps the EARLIEST CloudEvents
time seen (`LEAST`, ignoring NULLs), so arrival order never matters; the
projection can be dropped and rebuilt by replaying the topic under a new
consumer group.

```mermaid
erDiagram
    analytics_processed_events {
        TEXT event_id PK "every applied CloudEvents id, once"
        TEXT event_type
        TIMESTAMPTZ occurred_at "the projection's freshness"
        TIMESTAMPTZ applied_at
    }
    disposition_facts {
        TEXT disposition_id PK "one row per DispositionDecided"
        TEXT receipt_id
        BIGINT line_no
        TEXT sku
        BIGINT quantity
        TEXT grade
        TEXT outcome
        BIGINT policy_version
        TEXT supersedes
        TEXT reason
        TIMESTAMPTZ decided_at
    }
    return_line_facts {
        TEXT receipt_id PK
        BIGINT line_no PK
        TEXT site_id
        TEXT sku
        BIGINT quantity
        TEXT rma_id
        TEXT disposition_id
        TIMESTAMPTZ opened_at "opened or unexpected-received"
        TIMESTAMPTZ inspected_at
        TIMESTAMPTZ decided_at "earliest decision of the line"
        TIMESTAMPTZ staged_at "a RESTOCK was staged"
    }
```

A line is OPEN from `opened_at` until `staged_at`; the return cycle time is
`staged_at - opened_at` and exists only when the projection saw both.
Indexes: per-milestone partial indexes on `return_line_facts`,
`decided_at` and `(receipt_id, line_no)` on `disposition_facts`, and
`occurred_at` on `analytics_processed_events` for the freshness report.
