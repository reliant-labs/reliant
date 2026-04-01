-- +goose Up

-- Replace chat_id with thread_id on plans table for thread-scoped plans
ALTER TABLE plans ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';

-- Drop old column
ALTER TABLE plans DROP COLUMN chat_id;

-- Create new index
CREATE INDEX idx_plans_thread ON plans(thread_id);

-- +goose Down
ALTER TABLE plans ADD COLUMN chat_id TEXT NOT NULL DEFAULT '';
ALTER TABLE plans DROP COLUMN thread_id;
DROP INDEX IF EXISTS idx_plans_thread;
