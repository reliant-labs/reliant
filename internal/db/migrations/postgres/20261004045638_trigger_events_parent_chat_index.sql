-- +goose NO TRANSACTION
-- +goose Up
-- Serves ListRuns' parent_chat_id filter: the runs an agent started from one
-- chat. The parent lives only in the launch event's payload, so without this
-- the lookup scans every agent.start_run event. Partial on the kind, so the
-- index holds only agent-started runs.
--
-- Dropped then created CONCURRENTLY: a failed CONCURRENTLY build leaves an
-- INVALID index that IF NOT EXISTS would accept as finished.
DROP INDEX CONCURRENTLY IF EXISTS idx_trigger_events_parent_chat;
CREATE INDEX CONCURRENTLY idx_trigger_events_parent_chat ON trigger_events ((payload->>'parent_chat_id')) WHERE kind = 'agent.start_run';
