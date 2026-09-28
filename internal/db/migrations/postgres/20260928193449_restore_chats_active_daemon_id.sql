-- +goose Up

-- Restores chats.active_daemon_id for databases that lost it to the SECOND
-- version collision in this repo's history.
--
-- Version 20260321000000 was claimed by two files. #76 shipped
-- 20260321000000_add_metadata_to_yields.sql; #77 then added
-- 20260321000000_add_active_daemon_id_to_chats.sql under the same version, and
-- #82 renumbered the yields one to 20260321000001. A database migrated between
-- #76 and #77 recorded 20260321000000 as applied having run the YIELDS SQL, so
-- goose reports the active_daemon_id migration as already applied and its
-- ALTER never runs. Only a NEW version can add the column.
--
-- The other half of that collision needs no repair: the yields ALTER is now
-- guarded IF NOT EXISTS, and yields itself was dropped two days later by
-- 20260323000000_create_questions_table.sql.
--
-- active_daemon_id is session-level daemon targeting for a chat: when set, that
-- daemon is the default for tool execution (internal/db/core/chat.go,
-- internal/grpc/services/chat_helpers.go). A database missing it fails on
-- every chat read, not just on daemon routing.
--
-- Inert on every database that already has the column, which is all of them
-- outside that April window.
ALTER TABLE chats ADD COLUMN IF NOT EXISTS active_daemon_id TEXT;

-- Adding the column is NOT enough, and this is the subtle half.
--
-- chats_with_activity is defined as `SELECT c.*, ...  FROM chats c`, but
-- Postgres expands `c.*` ONCE, at CREATE VIEW time, and stores the expanded
-- column list. A view created while the column was missing therefore does not
-- gain it when the ALTER above runs — it keeps the frozen list forever. Every
-- later rebuild of this view (questions, timestamptz, paused-activity) is a
-- CREATE OR REPLACE, which cannot change a view's column set either, so
-- nothing downstream ever healed it.
--
-- That matters because the generated queries select the column by name FROM
-- THE VIEW (internal/db/postgres/generated/chats.sql.go), so on such a
-- database every chat read fails with "column active_daemon_id does not
-- exist" even once the table has it.
--
-- DROP + CREATE, not CREATE OR REPLACE, is what re-expands `c.*`. The body is
-- copied verbatim from 20260814120000_workflow_state_and_stop_reason.sql — the
-- CURRENT definition, which reads (state, stop_reason); the older
-- 20260801020000 body reads workflows.status, a column that migration dropped.
-- Re-expansion is the only change.
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
    END as activity
FROM chats c;
