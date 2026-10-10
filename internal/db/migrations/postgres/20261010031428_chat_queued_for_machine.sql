-- +goose Up

-- A message queued for a machine that never came up. A run whose machine
-- failed to start, was deleted, or was still not online at the wait's cap
-- ends; the message it was started for is not dropped. This column records
-- that it is still owed a reply: the run that delivers it is started
-- server-side the moment the chat's machine connects
-- (internal/queueddelivery), or by the user's next send. Cleared by whichever
-- of those starts the run, and by "Continue without machine", which moves the
-- conversation to a branch. See research/QUEUE_UNTIL_DAEMON.md.
--
-- Every statement is re-runnable (see 20261005170614).
ALTER TABLE chats ADD COLUMN IF NOT EXISTS queued_for_machine_at timestamptz;

-- The delivery sweep reads only the chats that hold a queued message.
CREATE INDEX IF NOT EXISTS idx_chats_queued_for_machine
    ON chats (queued_for_machine_at)
    WHERE queued_for_machine_at IS NOT NULL;

-- The view expands `c.*` at CREATE time, so the new chats column only appears
-- after a DROP + CREATE (see 20261003220140). Identical to 20261005170614
-- except for activity 6, QUEUED_FOR_MACHINE: no run is live and the chat holds
-- a message queued for its machine. It outranks ERROR because the failed run
-- that left it is exactly the run the queued message replaces, and it ranks
-- below every live-run state because a live run is the one delivering it.
-- `c.*` stays FIRST and the computed columns keep their order, because
-- generated queries scan `SELECT *` positionally.
DROP VIEW IF EXISTS chats_with_activity;

CREATE VIEW chats_with_activity AS
SELECT
    base.*,
    (
        base.launch_kind IS NULL
        OR base.launch_kind = 'chat.start'
        OR base.adopted_at IS NOT NULL
    ) AS list_in_sidebar,
    (CASE
        WHEN base.root_workflow_state IS NULL OR base.root_workflow_state = 1 THEN 1
        WHEN base.root_workflow_state = 2 AND base.activity = 2 THEN 3
        WHEN base.root_workflow_state = 2 AND base.activity = 5 THEN 8
        WHEN base.root_workflow_state = 2 THEN 2
        WHEN base.root_workflow_state = 3 AND base.root_workflow_stop_reason = 3 THEN 4
        WHEN base.root_workflow_state = 3 AND base.root_workflow_stop_reason = 1 THEN 5
        WHEN base.root_workflow_state = 3 AND base.root_workflow_stop_reason = 2 THEN 6
        WHEN base.root_workflow_state = 3 AND base.root_workflow_stop_reason = 4 THEN 7
        ELSE 0
    END)::integer AS display_state
FROM (
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
            WHEN c.daemon_blocked_at IS NOT NULL AND EXISTS (
                SELECT 1 FROM workflows w
                WHERE w.chat_id = c.id
                  AND w.state = 2
            ) THEN 5
            WHEN EXISTS (
                SELECT 1 FROM workflows w
                WHERE w.chat_id = c.id
                  AND w.state = 2
            ) THEN 1
            WHEN c.queued_for_machine_at IS NOT NULL THEN 6
            WHEN (
                SELECT MAX(w.completed_at) FILTER (WHERE w.state = 3 AND w.stop_reason = 2)
                FROM workflows w WHERE w.chat_id = c.id
            ) > COALESCE(
                (SELECT MAX(w.completed_at) FILTER (WHERE w.state = 3 AND w.stop_reason = 1)
                 FROM workflows w WHERE w.chat_id = c.id),
                '-infinity'::timestamptz
            ) THEN 3
            WHEN EXISTS (
                SELECT 1 FROM workflows w
                WHERE w.chat_id = c.id
                  AND w.state = 3 AND w.stop_reason = 3
            ) THEN 4
            ELSE 0
        END as activity,
        rw.state as root_workflow_state,
        rw.stop_reason as root_workflow_stop_reason,
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
    )
) base;
