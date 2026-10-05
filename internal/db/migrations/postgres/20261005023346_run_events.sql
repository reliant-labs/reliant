-- +goose Up

-- run_events is the outbox for workflow-event triggers: one row per run
-- transition another run may react to — a root run finishing, failing, or
-- becoming blocked on an approval or a question.
--
-- A row is written in the SAME transaction as the state it reports (the root
-- workflow's terminal status, the approval or question row), so the event is
-- exactly as durable as the transition: a worker that crashes after the commit
-- still leaves the event behind, and one that crashes before it leaves
-- neither. dedupe_key makes a retried activity a no-op.
--
-- Rows are only written for an owner who has an enabled workflow_event trigger;
-- everyone else's runs cost nothing here. A relay on the worker hands each row
-- to a dispatch workflow (claimed_until is its lease) and stamps dispatched_at.
CREATE TABLE IF NOT EXISTS run_events (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    chat_id text NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    workflow_name text NOT NULL DEFAULT '',
    outcome text NOT NULL CHECK (outcome IN ('finished', 'failed', 'blocked')),
    dedupe_key text NOT NULL UNIQUE,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL,
    claimed_until timestamptz,
    dispatched_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The relay's work queue: only undispatched rows, oldest first.
CREATE INDEX IF NOT EXISTS idx_run_events_undispatched ON run_events (created_at, id) WHERE dispatched_at IS NULL;

-- Retention prunes dispatched rows by age.
CREATE INDEX IF NOT EXISTS idx_run_events_dispatched_at ON run_events (dispatched_at) WHERE dispatched_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_run_events_chat ON run_events (chat_id);
