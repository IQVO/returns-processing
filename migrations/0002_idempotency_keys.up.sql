-- 0002_idempotency_keys.up.sql: the answers of the writes made with an
-- Idempotency-Key (the fleet's table pattern, see pick-face-replenishment).
-- Only 2xx and 4xx answers are stored, never 5xx; the first writer of a key
-- wins (ON CONFLICT DO NOTHING). fingerprint identifies the request (method,
-- path, body) so the same key reused with another request is told apart and
-- answered 409 idempotency-key-reused instead of replaying.

CREATE TABLE idempotency_keys (
    key          TEXT COLLATE "C" PRIMARY KEY,
    fingerprint  TEXT        NOT NULL,
    status       INTEGER     NOT NULL CHECK (status >= 200 AND status < 500),
    content_type TEXT        NOT NULL DEFAULT '',
    body         BYTEA       NOT NULL DEFAULT ''::bytea,
    created_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys (created_at);
