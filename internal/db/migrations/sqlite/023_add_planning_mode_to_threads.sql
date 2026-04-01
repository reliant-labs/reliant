-- +goose Up
-- Add planning mode flag to active threads for read-only tool filtering
ALTER TABLE active_threads ADD COLUMN is_planning_mode BOOLEAN NOT NULL DEFAULT FALSE;

-- Index for filtering queries by planning mode
CREATE INDEX idx_active_threads_planning_mode ON active_threads(chat_id, is_planning_mode);

-- +goose Down
DROP INDEX IF EXISTS idx_active_threads_planning_mode;
ALTER TABLE active_threads DROP COLUMN is_planning_mode;
