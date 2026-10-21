-- forge:breaks-rollback — chats_with_activity gains a column (working_threads), and the previous release scans that view with SELECT c.* into a struct that has no such field (store.ListArchivedChats), which bun rejects, so it cannot read the archived list until it is rebuilt against this schema.

-- A pending question or approval only makes a chat AWAITING_INPUT while the
-- run that asked it is still live. Before this migration any status=1 row did, which
-- let a question on a sub-agent that later completed, failed or was cancelled
-- pin its chat to "needs input" forever, outranking the threads still running
-- (prod chat adcee226).
--
-- pending_input_gates writes down once which run each pending approval and
-- question belongs to, so each reader applies its rule to the run that asked
-- rather than to the root. The run is the workflows row that asked:
-- questions.workflow_id when that row exists, else the newest row of the
-- asking thread (approvals carry only thread_id, and a resumed sub-agent gets
-- a new row on the same thread). `live` is the chat-activity rule: the run is
-- not STOPPED as COMPLETED (1), FAILED (2) or CANCELLED (4). A PAUSED (3) run
-- resumes into the same wait, so its gate stays live. A gate whose run cannot
-- be found (an approval with no thread) stays live: hiding a gate the user
-- could still answer is the worse failure, and the run-end cleanup resolves
-- those. (The inbox keeps a failed run's items, which an answer can still
-- feed when it resumes; it reads run_state / run_stop_reason for that.)
--
-- The rows stay pending in the table: resolving them when a thread ends is the
-- runtime's job. Derived here, not written, so a run that dies without cleaning
-- up cannot leave a chat stuck, the same rule as the usage-limit marker.
CREATE VIEW pending_input_gates AS
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
) g
LEFT JOIN workflows r ON r.id = g.run_id;

-- The chat's activity reads AWAITING_INPUT (2) only from a live gate, and the
-- view gains working_threads: how many of the chat's threads are doing work
-- right now. One activity value cannot say "needs you, and two sub-agents are
-- still running", which is what that chat was; working_threads is the second
-- dimension, orthogonal to activity. A thread is working when its run is
-- ACTIVE, it has no live gate of its own (it is waiting on the user), and no
-- run it issued is ACTIVE (it is waiting on its sub-agents, which count for
-- themselves). Without that last rule the parent of a sub-agent blocked on a
-- question would read as running, and "needs you" would nearly always claim
-- work was still going on.
--
-- DROP + CREATE, not CREATE OR REPLACE, as in 20261018213729: the new column
-- is appended LAST. Every other column is unchanged.
DROP VIEW IF EXISTS chats_with_activity;

CREATE VIEW chats_with_activity AS
SELECT
    base.id,
    base.title,
    base.project_id,
    base.user_id,
    base.state,
    base.workflow_id,
    base.run_id,
    base.created_at,
    base.updated_at,
    base.last_active,
    base.worktree_id,
    base.workflow_name,
    base.selected_presets,
    base.archived_worktree_name,
    base.unread,
    base.active_daemon_id,
    base.adopted_at,
    base.daemon_blocked_at,
    base.no_machine,
    base.queued_for_machine_at,
    base.last_message_at,
    base.activity,
    base.root_workflow_state,
    base.root_workflow_stop_reason,
    base.launch_kind,
    base.trigger_id,
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
    END)::integer AS display_state,
    base.usage_limit_resumes_at,
    base.usage_limit_provider,
    base.usage_limit_probing,
    base.working_threads
FROM (
    SELECT
        c.id,
        c.title,
        c.project_id,
        c.user_id,
        c.state,
        c.workflow_id,
        c.run_id,
        c.created_at,
        c.updated_at,
        c.last_active,
        c.worktree_id,
        c.workflow_name,
        c.selected_presets,
        c.archived_worktree_name,
        c.unread,
        c.active_daemon_id,
        c.adopted_at,
        c.daemon_blocked_at,
        c.no_machine,
        c.queued_for_machine_at,
        (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) AS last_message_at,
        CASE
            WHEN EXISTS (
                SELECT 1 FROM pending_input_gates g
                WHERE g.chat_id = c.id AND g.live
            ) THEN 2
            WHEN c.daemon_blocked_at IS NOT NULL AND EXISTS (
                SELECT 1 FROM workflows w
                WHERE w.chat_id = c.id
                  AND w.state = 2
            ) THEN 5
            WHEN ul.resume_at IS NOT NULL AND EXISTS (
                SELECT 1 FROM workflows w
                WHERE w.chat_id = c.id
                  AND w.state = 2
            ) THEN 7
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
        END AS activity,
        rw.state AS root_workflow_state,
        rw.stop_reason AS root_workflow_stop_reason,
        le.kind AS launch_kind,
        le.trigger_id AS trigger_id,
        ul.resume_at AS usage_limit_resumes_at,
        ul.provider AS usage_limit_provider,
        COALESCE(ul.reason = 'usage_limit_probe', false)::boolean AS usage_limit_probing,
        (
            SELECT count(DISTINCT w.thread)
            FROM workflows w
            WHERE w.chat_id = c.id
              AND w.state = 2
              AND NOT EXISTS (
                  SELECT 1 FROM pending_input_gates g
                  WHERE g.chat_id = c.id AND g.run_id = w.id
              )
              AND NOT EXISTS (
                  SELECT 1 FROM workflows ch
                  WHERE ch.parent_id = w.id AND ch.state = 2
              )
        )::integer AS working_threads
    FROM chats c
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    LEFT JOIN provider_backoff ul ON ul.thread_id = c.workflow_id
        AND ul.waiting_since IS NOT NULL
        AND ul.reason IN ('usage_limit', 'usage_limit_probe')
    LEFT JOIN trigger_events le ON le.id = (
        SELECT te.id
        FROM trigger_events te
        WHERE te.chat_id = c.id
        ORDER BY te.created_at ASC, te.id ASC
        LIMIT 1
    )
) base;

-- A view's comments are dropped with the view: the blocks of
-- 20261020232630_chats_with_activity_view_entity.up.sql and
-- 20261021115454_conversation_view_enum_go_types.up.sql, repeated.
COMMENT ON VIEW chats_with_activity IS 'forge:entity';
COMMENT ON COLUMN chats_with_activity.state IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ChatState';
COMMENT ON COLUMN chats_with_activity.root_workflow_state IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.WorkflowState';
COMMENT ON COLUMN chats_with_activity.root_workflow_stop_reason IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.WorkflowStopReason';
COMMENT ON COLUMN chats_with_activity.activity IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ChatActivity';
COMMENT ON COLUMN chats_with_activity.display_state IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.RunDisplayState';
