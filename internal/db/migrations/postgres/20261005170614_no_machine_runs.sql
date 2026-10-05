-- +goose Up

-- No-machine runs (research/DAEMONLESS_RUNS.md). A run with no machine is an
-- explicit choice recorded once, at launch, on the chat — never inferred from a
-- daemon being absent, which is the different case of a machine that is asleep
-- and will be woken. Every activity that could reach a machine already loads
-- the chat row, so this column is the run's whole execution context.
--
-- Every statement is re-runnable: the renumber-window repair re-applies
-- migrations against a database that already has their effects (see
-- access_tokens_renumber_repair_test.go and 20261004201053).
ALTER TABLE chats ADD COLUMN IF NOT EXISTS no_machine boolean NOT NULL DEFAULT false;

-- A trigger's machine becomes optional. no_machine is the explicit choice, and
-- a no-machine trigger names no daemon, so the two cannot disagree. The
-- converse (a daemon is required unless no_machine) is the TriggerService's
-- rule rather than a constraint: a row with neither cannot fire, and fails its
-- fire loudly, but rejecting it here would gain nothing over the write path's
-- own validation. A trigger whose pinned daemon was later deleted still names
-- it and fails the fire, rather than silently becoming a run with no machine.
ALTER TABLE triggers ADD COLUMN IF NOT EXISTS no_machine boolean NOT NULL DEFAULT false;
ALTER TABLE triggers ALTER COLUMN daemon_id DROP NOT NULL;
ALTER TABLE triggers DROP CONSTRAINT IF EXISTS triggers_no_machine_names_no_daemon_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_no_machine_names_no_daemon_check
    CHECK (NOT no_machine OR daemon_id IS NULL);

-- The view expands `c.*` at CREATE time, so the new chats column only appears
-- after a DROP + CREATE (see 20261003220140). Identical to
-- 20261004051031 otherwise: `c.*` stays FIRST and the computed columns keep
-- their order, because generated queries scan `SELECT *` positionally.
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
