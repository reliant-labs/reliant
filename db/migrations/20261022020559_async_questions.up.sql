-- forge:backward-compatible — additive column with a default, and pending_input_gates keeps its exact column list; the previous release reads questions by named columns and inserts without the column (so every row it writes is a blocking question, as before).

-- An async question (ask_user with async: true) is asked without waiting: the
-- asking thread carries on, and the user's answer is delivered to it later as
-- a message from the user. So it is never a gate. It stays out of
-- pending_input_gates — the chat never reads AWAITING_INPUT for it, and its
-- thread still counts as working — and the runtime does not close it when its
-- thread ends: it is an item in the user's inbox until it is answered.
ALTER TABLE questions ADD COLUMN async boolean NOT NULL DEFAULT false;

-- The same view as 20261021231046, with async questions left out. Same
-- columns in the same order, so CREATE OR REPLACE keeps chats_with_activity,
-- which reads it, valid.
CREATE OR REPLACE VIEW pending_input_gates AS
SELECT
    g.kind,
    g.id,
    g.chat_id,
    g.thread_id,
    g.run_id,
    r.state AS run_state,
    r.stop_reason AS run_stop_reason,
    (r.id IS NULL OR NOT (r.state = 3 AND r.stop_reason IN (1, 2, 4))) AS live,
    g.created_at
FROM (
    SELECT
        'approval'::text AS kind,
        a.id,
        a.chat_id,
        a.thread_id,
        a.created_at,
        (
            SELECT w.id FROM workflows w
            WHERE w.chat_id = a.chat_id AND w.thread = a.thread_id
            ORDER BY w.created_at DESC, w.id DESC
            LIMIT 1
        ) AS run_id
    FROM approvals a
    WHERE a.status = 1

    UNION ALL

    SELECT
        'question'::text,
        q.id,
        q.chat_id,
        NULLIF(q.thread_id, ''),
        q.created_at,
        COALESCE(
            (SELECT w.id FROM workflows w WHERE w.id = q.workflow_id AND w.chat_id = q.chat_id),
            (
                SELECT w.id FROM workflows w
                WHERE w.chat_id = q.chat_id AND w.thread = q.thread_id
                ORDER BY w.created_at DESC, w.id DESC
                LIMIT 1
            )
        )
    FROM questions q
    WHERE q.status = 1
      AND NOT q.async
) g
LEFT JOIN workflows r ON r.id = g.run_id;
