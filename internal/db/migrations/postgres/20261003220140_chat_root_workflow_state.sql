-- +goose Up

-- Exposes the chat's ROOT workflow lifecycle on chats_with_activity.
--
-- Chat.workflow_state / workflow_stop_reason (chat.proto 31/32) were never
-- populated: nothing in Go assigned them, so every chat went out on the wire
-- with both at zero. The web reads them (isWorkflowPaused in
-- web/src/lib/workflowLifecycle.ts), which made paused detection permanently
-- false, and a branched chat that has not started could not be distinguished
-- from a running one.
--
-- The root workflow is the row chats.workflow_id points at — not "any
-- workflow for this chat". That distinction is why this cannot reuse the
-- existing `activity` column: activity deliberately aggregates across threads
-- and spawns (a chat with a live spawn is RUNNING), whereas root state is
-- about the one run the chat's own lifecycle is. A chat whose root completed
-- while a spawn still runs is completed, and must not be offered as resumable.
--
-- It lives on the view rather than in a second query because every chat read
-- goes through this view (GetChat, GetChatWithUserCheck, ListChats,
-- SearchChats). A per-chat lookup would be an N+1 across a chat list, which is
-- precisely why the fields were left unset instead.
--
-- A chat with no root workflow row — a branch whose first run does not exist
-- yet — gets NULL from the LEFT JOIN, which maps to WORKFLOW_STATE_UNSPECIFIED
-- in Go. That is deliberately distinct from PENDING, which means the root row
-- exists and has not begun.

-- DROP + CREATE, never CREATE OR REPLACE. The view is `SELECT c.*, ... FROM
-- chats c`, and Postgres expands `c.*` ONCE at CREATE VIEW time and stores the
-- expanded column list; CREATE OR REPLACE cannot change a view's column set at
-- all, so it could neither add these two columns nor re-expand `c.*`. See
-- 20260928193449_restore_chats_active_daemon_id.sql, which is the repair for a
-- database that learned this the hard way.
--
-- `c.*` stays FIRST and the two new columns go LAST, after `activity`. The
-- generated queries are `SELECT *` against this view and scan positionally
-- (internal/db/postgres/generated/chats.sql.go), so appending is compatible
-- while inserting in the middle would silently misalign every chat read.
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
    rw.stop_reason as root_workflow_stop_reason
FROM chats c
LEFT JOIN workflows rw ON rw.id = c.workflow_id;
