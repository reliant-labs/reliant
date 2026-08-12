-- +goose Up
-- Stop a chat showing the red ERROR dot forever after it has recovered.
--
-- The previous rule was `EXISTS (workflow with status=4)` over the chat's
-- WHOLE history, with no recency bound. One failed workflow, ever, pinned the
-- chat to ERROR for the rest of its life — even after the user retried and
-- everything succeeded. The sidebar then sorted it up as "needs attention"
-- (priority += 400) permanently.
--
-- Observed on chat c0ce9449-b759-41c5-973d-80d1179de890: two spawned child
-- workflows failed at 19:11, and the twenty workflows that ran afterwards all
-- completed, the last of them five hours later at 00:10 — yet the view still
-- returned activity=3. Four of the five non-archived ERROR chats in the dev
-- database were in this recovered-but-still-red state.
--
-- The new rule: a chat is in ERROR only if NO workflow has completed
-- successfully since its most recent failure. That is what "recovered" means,
-- and it is deliberately stated in terms of completion TIME rather than "the
-- single latest terminal workflow":
--
--   - Spawns run concurrently, and a chat routinely has several workflows
--     finishing seconds apart. Looking only at whichever row happens to be
--     last would let an unrelated sibling that finished a moment later mask a
--     real failure.
--   - Comparing against the newest success means a failure is cleared only by
--     work that genuinely came after it, which is the user retrying and
--     getting a good result.
--
-- A failure with nothing successful after it still reads ERROR, so a chat that
-- is actually broken still surfaces. Only recovery clears it.
--
-- Ordering of the branches is unchanged: AWAITING_INPUT > RUNNING > ERROR >
-- PAUSED > IDLE.

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

        -- Running: any workflow for this chat is running (including threads/forks)
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 2  -- WorkflowStatusRunning
        ) THEN 1  -- RUNNING

        -- Error: a workflow failed and nothing has succeeded since.
        -- COALESCE on the success side makes "never succeeded" compare as the
        -- epoch, so an unrecovered failure still reports ERROR.
        WHEN (
            SELECT MAX(w.completed_at) FILTER (WHERE w.status = 4)  -- Failed
            FROM workflows w WHERE w.chat_id = c.id
        ) > COALESCE(
            (SELECT MAX(w.completed_at) FILTER (WHERE w.status = 3)  -- Completed
             FROM workflows w WHERE w.chat_id = c.id),
            '-infinity'::timestamptz
        ) THEN 3  -- ERROR

        -- Paused: any workflow for this chat is paused
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 6  -- WorkflowStatusPaused
        ) THEN 4  -- PAUSED

        ELSE 0  -- IDLE
    END as activity
FROM chats c;

-- +goose Down
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
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 4
        ) THEN 3
        WHEN EXISTS (
            SELECT 1 FROM workflows w
            WHERE w.chat_id = c.id
              AND w.status = 6
        ) THEN 4
        ELSE 0
    END as activity
FROM chats c;
