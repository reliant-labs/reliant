-- forge:backward-compatible — chats gains a NOT NULL column with a DEFAULT, so the previous release's chat inserts (which do not name it) still succeed; chats_with_activity only gains a column at its end, and every reader names the view's columns (store/columns.go), never c.*.

-- Who started a chat becomes a column of the chat: chats.origin, the enum
-- reliant.v1.ChatOrigin, written once at launch and never rewritten.
--
--   1 USER          a person typing (launch kind chat.start), a branch, or a
--                   chat that predates launch events
--   2 AUTOMATION    an automation's trigger: schedule, webhook, integration,
--                   workflow_event
--   3 AGENT         an agent's start_run (agent.start_run)
--   4 MANUAL_RUN    Run now (manual.run; adopted at birth)
--   5 BUILDER_TEST  a workflow builder's test run (builder.test)
--
-- Until now the chat list's policy was derived on every read from the kind of
-- the chat's FIRST trigger_events row (a lateral join), with "no event" and
-- "chat.start" meaning a conversation. That kept origin a side effect of
-- event bookkeeping, and SearchChats and ListChats without sidebar_only did
-- not apply it at all, so automation runs (the PR pipeline's pr-review,
-- cp-merge, release-*, sync-checkouts: tens an hour) reached chat surfaces.
-- The list policy is now keyed on this column.
--
-- The DEFAULT is USER only so the previous release can still insert while
-- both run; the launcher writes the origin of every chat it creates
-- (core.ChatOriginOf), and the backfill below sets every existing chat's from
-- its launch event, with the same mapping.
ALTER TABLE chats
    ADD COLUMN origin integer NOT NULL DEFAULT 1,
    ADD CONSTRAINT chats_origin_check CHECK (origin BETWEEN 1 AND 5);

COMMENT ON COLUMN chats.origin IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ChatOrigin';

-- Backfill from each chat's launch event: its first trigger_events row, the
-- one the view reads launch_kind from. A chat with no event keeps USER.
UPDATE chats c
SET origin = CASE le.kind
        WHEN 'chat.start' THEN 1
        WHEN 'agent.start_run' THEN 3
        WHEN 'manual.run' THEN 4
        WHEN 'builder.test' THEN 5
        ELSE 2 -- schedule, webhook, integration, workflow_event
    END
FROM (
    SELECT DISTINCT ON (te.chat_id) te.chat_id, te.kind
    FROM trigger_events te
    WHERE te.chat_id IS NOT NULL
    ORDER BY te.chat_id, te.created_at ASC, te.id ASC
) le
WHERE le.chat_id = c.id
  AND le.kind <> 'chat.start';

-- The view's sidebar policy reads the column: a USER chat, or a run the user
-- adopted. Every other column is unchanged; origin is appended last, so
-- CREATE OR REPLACE keeps the view (and its comments) in place.
CREATE OR REPLACE VIEW chats_with_activity AS
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
        base.origin = 1
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
    base.working_threads,
    base.origin
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
        )::integer AS working_threads,
        c.origin
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

COMMENT ON COLUMN chats_with_activity.origin IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ChatOrigin';
