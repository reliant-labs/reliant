-- +goose Up

-- Exposes a chat's ORIGIN on chats_with_activity: launch_kind ('chat.start',
-- 'schedule', ...) and trigger_id, taken from the chat's launch event — the
-- earliest trigger_events row with chat_id = c.id (idx_trigger_events_chat).
--
-- The triggers UI has to tell automation chats from interactive ones, and every
-- chat read goes through this view, so a per-chat lookup would be an N+1 across
-- the sidebar. trigger_id is NULL for ad hoc kinds and once the trigger is
-- deleted (ON DELETE SET NULL on trigger_events); both columns are NULL for a
-- chat that predates trigger events.
--
-- DROP + CREATE, never CREATE OR REPLACE: Postgres expands `c.*` once at
-- CREATE VIEW time and CREATE OR REPLACE cannot change a view's column set. See
-- 20261003220140_chat_root_workflow_state.sql. `c.*` stays FIRST and the new
-- columns go LAST — generated queries scan `SELECT *` positionally. The rest of
-- the body is unchanged from that migration.
DROP VIEW IF EXISTS chats_with_activity;

CREATE VIEW chats_with_activity AS
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    CASE
        -- Awaiting input: pending approvals or questions
        WHEN EXISTS (
            SELECT 1 FROM approvals a
            WHERE a.chat_id = c.id AND a.status = 1  -- APPROVAL_STATUS_PENDING
        ) THEN 2  -- AWAITING_INPUT
        WHEN EXISTS (
            SELECT 1 FROM questions q
            WHERE q.chat_id = c.id AND q.status = 1  -- QUESTION_STATUS_PENDING
        ) THEN 2  -- AWAITING_INPUT

        -- Running: any workflow for this chat is active (including threads/forks)
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.state = 2  -- ACTIVE
        ) THEN 1  -- RUNNING

        -- Error: a workflow failed and nothing has succeeded since.
        WHEN (
            SELECT MAX(w.completed_at) FILTER (WHERE w.state = 3 AND w.stop_reason = 2)  -- STOPPED/FAILED
            FROM workflows w WHERE w.chat_id = c.id
        ) > COALESCE(
            (SELECT MAX(w.completed_at) FILTER (WHERE w.state = 3 AND w.stop_reason = 1)  -- STOPPED/COMPLETED
             FROM workflows w WHERE w.chat_id = c.id),
            '-infinity'::timestamptz
        ) THEN 3  -- ERROR

        -- Paused: any workflow for this chat is parked awaiting resume
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.state = 3 AND w.stop_reason = 3  -- STOPPED/PAUSED
        ) THEN 4  -- PAUSED

        ELSE 0  -- IDLE
    END as activity,
    -- The root run's (state, stop_reason), read through chats.workflow_id.
    -- NULL for a chat with no root row; the store maps that to UNSPECIFIED.
    rw.state as root_workflow_state,
    rw.stop_reason as root_workflow_stop_reason,
    -- What started this chat, from its launch event. NULL for a chat that
    -- predates trigger events.
    le.kind as launch_kind,
    le.trigger_id as trigger_id
FROM chats c
LEFT JOIN workflows rw ON rw.id = c.workflow_id
LEFT JOIN trigger_events le ON le.id = (
    SELECT te.id
    FROM trigger_events te
    WHERE te.chat_id = c.id
    ORDER BY te.created_at ASC, te.id ASC
    LIMIT 1
);
