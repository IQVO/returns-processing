-- returns-processing analytics read model (ADR 0007).
--
-- This is the ANALYTICAL database (returns_processing_analytics), separate
-- from the OLTP database. It is written only by cmd/returns-projector and
-- read (read-only) by cmd/returns-reports. Everything here is a projection
-- derived from warehouse.returns-processing.analytics, not a source of
-- truth: it can be dropped and rebuilt by replaying the topic under a new
-- consumer group.
--
-- Facts, not aggregates: every number of the three reports is counted from
-- these rows over the requested window. Each milestone column is the
-- CloudEvents time of the EARLIEST such event seen (kept with LEAST, which
-- ignores NULLs), so arrival order never matters.

-- Idempotency: every applied CloudEvents id is recorded here exactly once, in
-- the SAME transaction as its effect. occurred_at is the event's CloudEvents
-- `time`; max(occurred_at) is the freshness of the projection.
CREATE TABLE analytics_processed_events (
    event_id    TEXT        PRIMARY KEY,
    event_type  TEXT        NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_analytics_processed_events_occurred_at ON analytics_processed_events (occurred_at);

-- disposition_facts: one row per DispositionDecided event (an override is
-- its own row, under its own disposition id): the cells of the
-- disposition-mix report.
CREATE TABLE disposition_facts (
    disposition_id TEXT COLLATE "C" PRIMARY KEY,
    receipt_id     TEXT COLLATE "C" NOT NULL,
    line_no        BIGINT      NOT NULL,
    sku            TEXT COLLATE "C",
    quantity       BIGINT      NOT NULL,
    grade          TEXT COLLATE "C" NOT NULL,
    outcome        TEXT COLLATE "C" NOT NULL,
    policy_version BIGINT      NOT NULL,
    supersedes     TEXT COLLATE "C",
    reason         TEXT COLLATE "C",
    decided_at     TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_disposition_facts_decided_at ON disposition_facts (decided_at);
CREATE INDEX idx_disposition_facts_line       ON disposition_facts (receipt_id, line_no);

-- return_line_facts: one row per receipt LINE the projection has seen (any
-- of its events), one column per milestone.
--   opened_at     ReturnReceiptOpened (per carried line) or
--                 UnexpectedReturnReceived (its own line)
--   inspected_at  ReturnLineInspected
--   decided_at    DispositionDecided (the earliest decision of the line)
--   staged_at     ReturnLineStaged (a RESTOCK was staged for put-away)
-- A line is OPEN from opened_at until staged_at is set; the return cycle
-- time is staged_at - opened_at and exists only when the projection saw
-- both. The descriptive columns keep the first non-NULL value, so the
-- events may arrive in any order.
CREATE TABLE return_line_facts (
    receipt_id     TEXT COLLATE "C" NOT NULL,
    line_no        BIGINT      NOT NULL,
    site_id        TEXT COLLATE "C",
    sku            TEXT COLLATE "C",
    quantity       BIGINT,
    rma_id         TEXT COLLATE "C",
    disposition_id TEXT COLLATE "C",
    opened_at      TIMESTAMPTZ,
    inspected_at   TIMESTAMPTZ,
    decided_at     TIMESTAMPTZ,
    staged_at      TIMESTAMPTZ,
    PRIMARY KEY (receipt_id, line_no)
);

CREATE INDEX idx_return_line_facts_site      ON return_line_facts (site_id);
CREATE INDEX idx_return_line_facts_opened_at ON return_line_facts (opened_at) WHERE opened_at IS NOT NULL;
CREATE INDEX idx_return_line_facts_decided_at ON return_line_facts (decided_at) WHERE decided_at IS NOT NULL;
CREATE INDEX idx_return_line_facts_staged_at ON return_line_facts (staged_at) WHERE staged_at IS NOT NULL;
