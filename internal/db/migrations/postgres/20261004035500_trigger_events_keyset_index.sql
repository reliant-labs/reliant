-- +goose NO TRANSACTION
-- +goose Up
-- Serves ListTriggerEvents: one trigger's firings newest-first, paged by keyset
-- on (occurred_at, id). The old (trigger_id, occurred_at) index cannot satisfy
-- the id tiebreak, so tied rows needed a sort; this one is a pure range scan and
-- also serves the per-trigger window in ListRecentTriggerFirings. It supersedes
-- the old index, which is dropped.
--
-- Dropped then created CONCURRENTLY: a failed CONCURRENTLY build leaves an
-- INVALID index that IF NOT EXISTS would accept as finished.
DROP INDEX CONCURRENTLY IF EXISTS idx_trigger_events_trigger_occurred_id;
CREATE INDEX CONCURRENTLY idx_trigger_events_trigger_occurred_id ON trigger_events (trigger_id, occurred_at DESC, id DESC);
DROP INDEX CONCURRENTLY IF EXISTS idx_trigger_events_trigger_occurred;
