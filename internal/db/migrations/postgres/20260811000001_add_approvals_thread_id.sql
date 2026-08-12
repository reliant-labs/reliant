-- +goose Up
--
-- approvals has no thread_id, so an approval raised inside a sub-agent cannot
-- be attributed to it -- with several agents live at once, nothing can say
-- which one is gated (spec: async-spawn-and-agent-messaging.md, §7.6).
--
-- Nullable, and NOT backfilled: historical rows were never associated with a
-- thread, and there is nothing to derive one from after the fact.
-- ON DELETE RESTRICT mirrors the existing tool_calls.thread_id FK
-- (20260801010000): an approval is a durable audit record, so the thread it
-- names must not be deletable out from under it.
ALTER TABLE approvals ADD COLUMN thread_id text REFERENCES threads(id) ON DELETE RESTRICT;

CREATE INDEX idx_approvals_thread_id ON approvals(thread_id) WHERE thread_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_approvals_thread_id;
ALTER TABLE approvals DROP COLUMN IF EXISTS thread_id;
