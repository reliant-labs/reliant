-- +goose Up

-- G10 (WORKFLOW_UI.md §6.4): per-automation opt-in to be told when a run
-- finishes. Unattended completions are silent by default (decision 9).
ALTER TABLE triggers ADD COLUMN IF NOT EXISTS notify_on_complete boolean NOT NULL DEFAULT false;
