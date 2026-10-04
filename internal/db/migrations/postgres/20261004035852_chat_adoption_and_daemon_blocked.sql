-- +goose Up

-- G5 (adoption) and G7 (daemon pending) from research/WORKFLOW_UI.md §13.
--
-- adopted_at: the user took an automation run into their own chats. Origin
-- (launch_kind, trigger_id) is history and is never rewritten; adoption is a
-- separate fact. NULL = not adopted.
--
-- daemon_blocked_at: set when a tool call on this chat's run failed because the
-- machine it needs is suspended or still starting (toolexec.ErrDaemonPending),
-- cleared by the next successful tool call. The view only reports it while a
-- workflow of the chat is ACTIVE, so a run that stopped cannot leave a stale
-- "waiting for machine" behind.
ALTER TABLE chats ADD COLUMN IF NOT EXISTS adopted_at timestamptz;
ALTER TABLE chats ADD COLUMN IF NOT EXISTS daemon_blocked_at timestamptz;

-- Rebuild chats_with_activity. DROP + CREATE, never CREATE OR REPLACE: Postgres
-- expands `c.*` once at CREATE VIEW time (see 20261003220140). `c.*` stays
-- FIRST and new computed columns go LAST — generated queries scan `SELECT *`
-- positionally.
--
-- activity gains 5 = WAITING_FOR_DAEMON. Precedence: AWAITING_INPUT (2) beats
-- WAITING_FOR_DAEMON (5), which beats RUNNING (1).
--
-- list_in_sidebar is the one definition of "does this chat belong in the
-- sidebar chat list": interactive or unknown-origin chats, adopted chats, and —
-- until the Inbox ships (WORKFLOW_UI.md §14.1 decision 5) — a non-agent
-- automation awaiting input. An agent.start_run chat is listed only if adopted
-- (decision 4).
DROP VIEW IF EXISTS chats_with_activity;

CREATE VIEW chats_with_activity AS
SELECT
    base.*,
    (
        base.launch_kind IS NULL
        OR base.launch_kind = 'chat.start'
        OR base.adopted_at IS NOT NULL
        OR (base.launch_kind <> 'agent.start_run' AND base.activity = 2)
    ) AS list_in_sidebar,
    -- The one RunDisplayState derivation. ListRuns, LastRunPerWorkflow,
    -- ListTriggerEvents and ListRecentTriggerFirings all read this column, so
    -- they cannot disagree. 1 queued, 2 running, 3 needs input, 4 paused,
    -- 5 completed, 6 failed, 7 cancelled, 8 waiting for machine.
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
            -- Awaiting input: pending approvals or questions
            WHEN EXISTS (
                SELECT 1 FROM approvals a
                WHERE a.chat_id = c.id AND a.status = 1  -- APPROVAL_STATUS_PENDING
            ) THEN 2  -- AWAITING_INPUT
            WHEN EXISTS (
                SELECT 1 FROM questions q
                WHERE q.chat_id = c.id AND q.status = 1  -- QUESTION_STATUS_PENDING
            ) THEN 2  -- AWAITING_INPUT

            -- Waiting for daemon: a live run whose last tool call hit a machine
            -- that is suspended or still starting.
            WHEN c.daemon_blocked_at IS NOT NULL AND EXISTS (
                SELECT 1 FROM workflows w
                WHERE w.chat_id = c.id
                  AND w.state = 2  -- ACTIVE
            ) THEN 5  -- WAITING_FOR_DAEMON

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
