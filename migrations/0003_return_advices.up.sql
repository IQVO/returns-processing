-- 0003_return_advices.up.sql: the network return-advice view (ADR 0012) —
-- the poller-fed local read model of the retail network's return
-- authorizations, keyed authorizationId, together with this service's
-- outcome submission state. A read model, not an aggregate: no version
-- column, no outbox rows, no events.

CREATE TABLE return_advice_view (
    authorization_id  TEXT COLLATE "C" PRIMARY KEY,
    order_id          TEXT COLLATE "C" NOT NULL DEFAULT '',
    group_no          BIGINT      NOT NULL DEFAULT 0,
    vendor_id         TEXT COLLATE "C" NOT NULL,
    node_id           TEXT COLLATE "C" NOT NULL,
    -- The network's authorization-level decision, recorded as an INPUT
    -- (ADR 0012): never re-decided, never branched on.
    disposition       TEXT         NOT NULL CHECK (disposition IN ('KEEP', 'NEAREST_RETURNS_CENTER', 'ORIGIN_NODE')),
    method            TEXT         NOT NULL CHECK (method IN ('DROP_OFF', 'PICKUP')),
    state             TEXT         NOT NULL CHECK (state IN ('AUTHORIZED', 'CANCELLED', 'OUTCOME_RECORDED')),
    lines             JSONB        NOT NULL CHECK (jsonb_typeof(lines) = 'array'),
    tracking_numbers  JSONB        NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(tracking_numbers) = 'array'),
    ready_at          TIMESTAMPTZ  NOT NULL,
    expires_at        TIMESTAMPTZ,
    first_seen_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- This service's outcome submission state (ADR 0012): NULL until a
    -- receipt opened against the advice completes, then PENDING -> SUBMITTED
    -- -> SUCCEEDED | FAILED (both terminal). The poller's upsert NEVER
    -- touches these columns.
    outcome_state          TEXT CHECK (outcome_state IN ('PENDING', 'SUBMITTED', 'SUCCEEDED', 'FAILED')),
    outcome_node_id        TEXT COLLATE "C",
    outcome_received_at    TIMESTAMPTZ,
    outcome_lines          JSONB,
    outcome_transaction_id TEXT COLLATE "C",
    outcome_errors         TEXT NOT NULL DEFAULT '',
    outcome_since          TIMESTAMPTZ
);

-- PendingOutcomes: the drain's work list (submit or settle), oldest first.
CREATE INDEX idx_return_advice_view_pending
    ON return_advice_view (outcome_since)
    WHERE outcome_state IN ('PENDING', 'SUBMITTED');

-- LatestReadyAt: the poll watermark.
CREATE INDEX idx_return_advice_view_ready_at
    ON return_advice_view (ready_at DESC);
