-- 0001_returns-processing.up.sql: returns-processing's OLTP schema
-- (ADR 0002 aggregates, ADR 0003 local copies, the fleet's shared outbox and
-- consumer-dedupe tables).
--
-- Text keys that are compared or ordered in Go (ids, SKUs, sites, order refs)
-- use the "C" collation so ORDER BY and key comparisons follow byte order, the
-- same order the in-memory adapters use.

-- return_authorizations: one row per ReturnAuthorization (RMA) aggregate, the
-- whole snapshot. version is 0 while REQUESTED, 1 when decided and +1 per
-- accepted transition, and guards every write (optimistic concurrency,
-- mirrors inventory-storage ADR 0018/0019).
CREATE TABLE return_authorizations (
    rma_id         TEXT COLLATE "C" PRIMARY KEY,
    order_id       TEXT COLLATE "C" NOT NULL,
    line_no        INTEGER      NOT NULL CHECK (line_no >= 1),
    sku            TEXT COLLATE "C" NOT NULL,
    quantity       BIGINT       NOT NULL CHECK (quantity >= 1),
    site_id        TEXT COLLATE "C" NOT NULL,
    reason_code    TEXT         NOT NULL CHECK (reason_code <> ''),
    status         TEXT         NOT NULL CHECK (status IN ('REQUESTED', 'AUTHORIZED', 'REJECTED', 'RECEIVED', 'CLOSED')),
    rejection_code TEXT         NOT NULL DEFAULT '' CHECK (rejection_code IN ('', 'LINE_NOT_MANIFESTED', 'RETURN_WINDOW_EXPIRED', 'OPEN_RMA_EXISTS', 'QUANTITY_EXCEEDS_POLICY')),
    window_ends_at TIMESTAMPTZ,
    decided_at     TIMESTAMPTZ,
    received_at    TIMESTAMPTZ,
    closed_at      TIMESTAMPTZ,
    version        BIGINT       NOT NULL CHECK (version >= 0),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Invariant 1 (ADR 0002): at most ONE open RMA per (order_id, line_no). The
-- rule spans aggregates, so the use case checks it and this partial unique
-- index backs it: the losing insert of a race fails here and becomes the
-- usecases.ErrConcurrentModification / ErrOpenRMAExists mapping.
CREATE UNIQUE INDEX uq_rmas_one_open_per_order_line
    ON return_authorizations (order_id, line_no)
    WHERE status IN ('REQUESTED', 'AUTHORIZED', 'RECEIVED');

-- FindOpenByOrderLine and ListByOrder.
CREATE INDEX idx_rmas_by_order ON return_authorizations (order_id, line_no);

-- return_receipts / return_receipt_lines: one ReturnReceipt aggregate split
-- root/lines (the lines are persisted with the root in the same write; the
-- child table has no version of its own -- the root's version guards the
-- whole aggregate). version is 1 when opened and +1 per inspected line.
CREATE TABLE return_receipts (
    receipt_id TEXT COLLATE "C" PRIMARY KEY,
    rma_id     TEXT COLLATE "C" NOT NULL DEFAULT '',
    site_id    TEXT COLLATE "C" NOT NULL,
    opened_at  TIMESTAMPTZ  NOT NULL,
    version    BIGINT       NOT NULL CHECK (version >= 1),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE return_receipt_lines (
    receipt_id         TEXT COLLATE "C" NOT NULL REFERENCES return_receipts (receipt_id) ON DELETE CASCADE,
    line_no            INTEGER      NOT NULL CHECK (line_no >= 1),
    sku                TEXT COLLATE "C" NOT NULL,
    quantity           BIGINT       NOT NULL CHECK (quantity >= 1),
    kind               TEXT         NOT NULL CHECK (kind IN ('MATCHED', 'UNEXPECTED')),
    rma_id             TEXT COLLATE "C" NOT NULL DEFAULT '',
    inspected          BOOLEAN      NOT NULL DEFAULT false,
    inspected_quantity BIGINT       NOT NULL DEFAULT 0 CHECK (inspected_quantity >= 0),
    grade              TEXT         NOT NULL DEFAULT '' CHECK (grade IN ('', 'A', 'B', 'C', 'SCRAP')),
    reason_codes       JSONB        NOT NULL DEFAULT '[]'::jsonb,
    inspected_at       TIMESTAMPTZ,
    PRIMARY KEY (receipt_id, line_no),
    -- a MATCHED line carries its rma id, an UNEXPECTED one carries none: the
    -- refused third state cannot even be stored.
    CHECK ((kind = 'MATCHED') = (rma_id <> ''))
);

-- dispositions: one row per immutable Disposition decision (version is always
-- 1; an override is a NEW row carrying supersedes). staged records that the
-- ReturnLineStaged fact was raised (exactly once per disposition).
CREATE TABLE dispositions (
    disposition_id TEXT COLLATE "C" PRIMARY KEY,
    receipt_id     TEXT COLLATE "C" NOT NULL,
    line_no        INTEGER      NOT NULL CHECK (line_no >= 1),
    sku            TEXT COLLATE "C" NOT NULL,
    quantity       BIGINT       NOT NULL CHECK (quantity >= 1),
    grade          TEXT         NOT NULL CHECK (grade IN ('A', 'B', 'C', 'SCRAP')),
    outcome        TEXT         NOT NULL CHECK (outcome IN ('RESTOCK', 'REFURBISH', 'LIQUIDATE', 'DISPOSE', 'RTV')),
    policy_version BIGINT       NOT NULL CHECK (policy_version >= 1),
    supersedes     TEXT COLLATE "C",
    reason         TEXT         NOT NULL DEFAULT '',
    site_id        TEXT COLLATE "C" NOT NULL,
    rma_id         TEXT COLLATE "C" NOT NULL DEFAULT '',
    received_at    TIMESTAMPTZ  NOT NULL,
    decided_at     TIMESTAMPTZ  NOT NULL,
    staged         BOOLEAN      NOT NULL DEFAULT false,
    version        BIGINT       NOT NULL CHECK (version = 1),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- At most one POLICY decision per receipt line: an override (supersedes set)
-- is not in the index, so a line can be superseded any number of times while
-- its non-superseding decision stays unique.
CREATE UNIQUE INDEX uq_dispositions_one_policy_decision_per_line
    ON dispositions (receipt_id, line_no)
    WHERE supersedes IS NULL;

-- FindByReceiptLine (the line's latest decision) and audit.
CREATE INDEX idx_dispositions_by_line ON dispositions (receipt_id, line_no, decided_at DESC);

-- disposition_policies: the DispositionPolicy versions (ADR 0002: a change
-- is a new version, never a rewrite of an old one). The version IS the
-- primary key. is_current marks the latest declared version (at most one);
-- Save clears the previous current and upserts the new one in one
-- transaction (a re-declared version replaces its own row, the exact
-- semantics the use cases rely on when a table is reset).
CREATE TABLE disposition_policies (
    version            BIGINT      PRIMARY KEY CHECK (version >= 1),
    return_window_days INTEGER     NOT NULL CHECK (return_window_days >= 0),
    max_units_per_line BIGINT      NOT NULL CHECK (max_units_per_line >= 1),
    veto_outcome       TEXT        NOT NULL CHECK (veto_outcome IN ('RESTOCK', 'REFURBISH', 'LIQUIDATE', 'DISPOSE', 'RTV')),
    rules              JSONB       NOT NULL CHECK (jsonb_typeof(rules) = 'array'),
    declared_at        TIMESTAMPTZ NOT NULL,
    is_current         BOOLEAN     NOT NULL DEFAULT false
);

CREATE UNIQUE INDEX uq_disposition_policies_one_current
    ON disposition_policies (is_current) WHERE is_current;

-- processed_events: the inbound consumers' idempotency guard. event_id is the
-- CloudEvents id; the row is claimed with INSERT ... ON CONFLICT DO NOTHING in
-- the SAME transaction as the effect, so a rollback un-claims it.
CREATE TABLE processed_events (
    event_id     TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- outbox: the transactional outbox (the relay table). One row per domain
-- fact, inserted in the SAME transaction as the aggregate; the relay drains
-- due rows to Kafka as CloudEvents. payload is a self-describing JSON object
-- {"type": <EventName>, "data": {pinned data fields}}; the relay derives the
-- CloudEvents type/subject/dataschema from aggreg_type + payload. event_id is
-- the CloudEvents id, minted once at enqueue time and persisted: a relay
-- retry republishes the same id (the fleet dedupe contract).
CREATE TABLE outbox (
    id              BIGSERIAL   PRIMARY KEY,
    event_id        TEXT        NOT NULL UNIQUE,
    aggreg_type     TEXT        NOT NULL,
    aggreg_id       TEXT COLLATE "C" NOT NULL,
    payload         JSONB       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The relay scans for due retries: few attempts, earliest next attempt first.
CREATE INDEX idx_outbox_due ON outbox (attempts, next_attempt_at);

-- allocated_lines: the order-management local copy (ADR 0003), one row per
-- allocated order line, fed by OrderAllocated / OrderPartiallyAllocated. The
-- sources carry no version, so a newer fact is last-write-wins and version is
-- the local write counter.
CREATE TABLE allocated_lines (
    order_id   TEXT COLLATE "C" NOT NULL,
    line_no    INTEGER     NOT NULL CHECK (line_no >= 1),
    sku        TEXT COLLATE "C" NOT NULL,
    version    BIGINT      NOT NULL DEFAULT 1 CHECK (version >= 1),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (order_id, line_no)
);

-- manifested_units: the fulfillment-execution package-manifest local copy
-- (ADR 0003), one row per manifested WES work unit
-- `{order_id}-line-{line_no}`, also indexed by the physical package id.
CREATE TABLE manifested_units (
    work_unit     TEXT COLLATE "C" PRIMARY KEY,
    order_id      TEXT COLLATE "C" NOT NULL,
    line_no       INTEGER     NOT NULL CHECK (line_no >= 1),
    package_id    TEXT COLLATE "C",
    manifested_at TIMESTAMPTZ NOT NULL,
    version       BIGINT      NOT NULL DEFAULT 1 CHECK (version >= 1),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_manifested_units_package UNIQUE (package_id)
);

-- product_classes: the product-master classification local copy (ADR 0003),
-- one row per SKU, fed by ProductClassified. The source carries a version; a
-- fact is applied only when its version is greater than the stored one.
CREATE TABLE product_classes (
    sku               TEXT COLLATE "C" PRIMARY KEY,
    tags              JSONB       NOT NULL DEFAULT '[]'::jsonb,
    temperature_class TEXT        NOT NULL DEFAULT '',
    version           BIGINT      NOT NULL CHECK (version >= 1),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
