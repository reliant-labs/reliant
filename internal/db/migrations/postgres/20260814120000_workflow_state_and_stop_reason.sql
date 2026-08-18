-- +goose Up
-- Split workflows.status into (state, stop_reason).
--
-- The old eight-value enum conflated where a run IS with why it stopped:
--
--     PENDING RUNNING COMPLETED FAILED CANCELLED PAUSED EXPIRED
--
-- COMPLETED / FAILED / CANCELLED / PAUSED are not four states; they are one
-- state (stopped) reached four ways, and callers were asking "did it stop"
-- and "can it continue" by enumerating subsets of the enum at 161 sites. The
-- new shape answers those two questions directly:
--
--     state       1 PENDING | 2 ACTIVE | 3 STOPPED
--     stop_reason 1 COMPLETED | 2 FAILED | 3 PAUSED | 4 CANCELLED  (STOPPED only)
--
-- EXPIRED (old 7) is NOT carried over. Nothing ever wrote it: Temporal
-- TIMED_OUT is deliberately mapped to FAILED by both status mappers, so the
-- value was read in four places and set in none. The dev database confirms it
-- — zero rows out of 1,077. The backfill below therefore maps six values, and
-- any stray 7 is folded into FAILED, which is exactly how the code that read
-- it already behaved.
--
-- The old column is dropped here rather than deprecated: this converts in one
-- migration, and no reader of `status` survives the change.

ALTER TABLE workflows
    ADD COLUMN state       integer NOT NULL DEFAULT 2,  -- ACTIVE, mirroring the old status default
    ADD COLUMN stop_reason integer NOT NULL DEFAULT 0;  -- UNSPECIFIED

UPDATE workflows SET
    state = CASE status
        WHEN 1 THEN 1  -- PENDING   -> PENDING
        WHEN 2 THEN 2  -- RUNNING   -> ACTIVE
        ELSE 3         -- COMPLETED/FAILED/CANCELLED/PAUSED/EXPIRED -> STOPPED
    END,
    stop_reason = CASE status
        WHEN 3 THEN 1  -- COMPLETED
        WHEN 4 THEN 2  -- FAILED
        WHEN 5 THEN 4  -- CANCELLED
        WHEN 6 THEN 3  -- PAUSED
        WHEN 7 THEN 2  -- EXPIRED -> FAILED (never written; resumed identically)
        ELSE 0         -- PENDING/RUNNING carry no stop reason
    END;

-- The activity view reads workflow lifecycle; repoint it before the column it
-- depends on disappears. Semantics are preserved exactly, branch for branch:
-- AWAITING_INPUT > RUNNING > ERROR > PAUSED > IDLE, with ERROR still cleared
-- by a success that completed after the most recent failure.
CREATE OR REPLACE VIEW chats_with_activity AS
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
    END as activity
FROM chats c;

DROP INDEX IF EXISTS idx_workflows_status;
ALTER TABLE workflows DROP COLUMN status;

-- Lifecycle lookups are always by state, sometimes narrowed by reason
-- (ListWorkflowsByStatus, the cascades, the reconciler's sweeps).
CREATE INDEX idx_workflows_state ON workflows(state, stop_reason);

-- +goose Down
ALTER TABLE workflows ADD COLUMN status integer NOT NULL DEFAULT 2;

UPDATE workflows SET status = CASE
    WHEN state = 1 THEN 1  -- PENDING
    WHEN state = 2 THEN 2  -- ACTIVE -> RUNNING
    ELSE CASE stop_reason
        WHEN 1 THEN 3  -- COMPLETED
        WHEN 2 THEN 4  -- FAILED
        WHEN 3 THEN 6  -- PAUSED
        WHEN 4 THEN 5  -- CANCELLED
        ELSE 3
    END
END;

CREATE OR REPLACE VIEW chats_with_activity AS
SELECT
    c.*,
    (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) as last_message_at,
    CASE
        WHEN EXISTS (
            SELECT 1 FROM approvals a
            WHERE a.chat_id = c.id AND a.status = 1
        ) THEN 2
        WHEN EXISTS (
            SELECT 1 FROM questions q
            WHERE q.chat_id = c.id AND q.status = 1
        ) THEN 2
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 2
        ) THEN 1
        WHEN (
            SELECT MAX(w.completed_at) FILTER (WHERE w.status = 4)
            FROM workflows w WHERE w.chat_id = c.id
        ) > COALESCE(
            (SELECT MAX(w.completed_at) FILTER (WHERE w.status = 3)
             FROM workflows w WHERE w.chat_id = c.id),
            '-infinity'::timestamptz
        ) THEN 3
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 6
        ) THEN 4
        ELSE 0
    END as activity
FROM chats c;

DROP INDEX IF EXISTS idx_workflows_state;
ALTER TABLE workflows DROP COLUMN state, DROP COLUMN stop_reason;
CREATE INDEX idx_workflows_status ON workflows(status);
