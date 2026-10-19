-- forge:breaks-rollback — chats_with_activity gains columns (and a new activity value), and the previous release scans that view positionally with SELECT *, so it cannot read the rows until it is rebuilt against this schema.

-- A run held until a provider's usage limit resets. The wait is a per-thread
-- durable marker, written BEFORE the workflow parks on its timer, so
-- `workflow ps` and the web can tell waiting from wedged. It reuses
-- provider_backoff, the table every other provider wait already writes:
--   reason 'usage_limit'        the reset time is known; resume_at is that time
--                               plus a small margin.
--   reason 'usage_limit_probe'  the provider named no reset; resume_at is the
--                               next probe on the unbounded ladder.
-- `provider` names who imposed the limit, for the "Waiting for <provider>
-- usage limit" line; the driver's in-activity ladder leaves it empty.
ALTER TABLE provider_backoff ADD COLUMN provider text NOT NULL DEFAULT '';

-- The chat's activity gains 7 = WAITING_FOR_USAGE_LIMIT: a live run whose ROOT
-- thread is parked on a usage-limit marker. Derived from the marker, not stored
-- on chats, so there is one source of truth and a run that dies mid-wait cannot
-- leave a chat stuck: the marker only counts while the root workflow is running.
-- Sub-agent waits do not change the chat's activity; the parent sees them in
-- spawn_status. Precedence: AWAITING_INPUT (2) > WAITING_FOR_DAEMON (5) >
-- WAITING_FOR_USAGE_LIMIT (7) > RUNNING (1).
--
-- DROP + CREATE, not CREATE OR REPLACE: databases that went through the renumber
-- window carry the view with a different column order, which OR REPLACE refuses.
-- The new columns are appended LAST (after display_state): generated queries scan
-- SELECT * positionally.
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
    base.usage_limit_probing
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
        COALESCE(ul.reason = 'usage_limit_probe', false)::boolean AS usage_limit_probing
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
