-- 0001_returns-processing.down.sql: drops the whole schema of 0001, in the
-- reverse dependency order (children first, the FK target last).
DROP TABLE IF EXISTS return_receipt_lines;
DROP TABLE IF EXISTS return_receipts;
DROP TABLE IF EXISTS return_authorizations;
DROP TABLE IF EXISTS dispositions;
DROP TABLE IF EXISTS disposition_policies;
DROP TABLE IF EXISTS processed_events;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS allocated_lines;
DROP TABLE IF EXISTS manifested_units;
DROP TABLE IF EXISTS product_classes;
