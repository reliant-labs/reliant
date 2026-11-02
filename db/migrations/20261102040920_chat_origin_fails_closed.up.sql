-- forge:backward-compatible — chats.origin only loses its NOT NULL and its DEFAULT, so every release's inserts still succeed (one that does not name the column now stores NULL instead of USER); chats_with_activity keeps every column's name, position, type and, for every chat a release that writes origin created, value.

-- Migration: chat_origin_fails_closed
--
-- chats.origin (20261030033817_chat_origin) is the chat list's policy key: a
-- USER chat lists, any other origin is a run that lists only once adopted. Its
-- DEFAULT was USER "so the previous release can still insert while both run".
-- That made the policy fail open: every chat a release that predates origin
-- inserted became a person's conversation, whatever launched it.
--
-- Prod hit it on 2026-10-31. The api-server moved to a release that writes
-- origin (and ran that migration) at 19:27 UTC, and the temporal worker, which
-- launches every trigger's run, went back to the 2026-10-29 image. From then
-- on each run the PR pipeline fired (pr-review, cp-merge, pr-sweep, ...;
-- about 85 an hour) was inserted by the old worker, defaulted to USER, and
-- landed in the owner's chat list, its Archived tab and chat search.
--
-- A chat's launch event already says who started it, and every chat a launch
-- creates has one: the launcher inserts the event in the same transaction.
-- So an origin the inserting release did not record is NULL now, not a guess,
-- and chats_with_activity reads it from the launch event, with the mapping
-- core.ChatOriginOf uses (chat_origin_of_launch_kind below; a test holds the
-- two equal). The column stays the record for every chat a current release
-- creates (the launcher and BranchChat always name it), and the view only
-- derives the rows nobody classified; COALESCE evaluates the launch-event
-- probe for those rows alone.

-- The origin of a chat launched by an event of this kind: core.ChatOriginOf,
-- plus NULL (no launch event: a branch, or a chat that predates launch
-- events) as a person's. Unknown kinds are an automation's, as in Go.
CREATE OR REPLACE FUNCTION chat_origin_of_launch_kind(kind text)
RETURNS integer
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN kind IS NULL OR kind = 'chat.start' THEN 1
        WHEN kind = 'agent.start_run' THEN 3
        WHEN kind = 'manual.run' THEN 4
        WHEN kind = 'builder.test' THEN 5
        WHEN kind = 'cli.run' THEN 6
        ELSE 2 -- schedule, webhook, integration, workflow_event
    END
$$;

ALTER TABLE chats
    ALTER COLUMN origin DROP DEFAULT,
    ALTER COLUMN origin DROP NOT NULL;

-- Re-classify the chats the default mislabelled: USER, but launched by
-- something other than a person typing. Only a release that predates origin
-- can have written that pair (the launcher maps chat.start, and nothing
-- else, to USER; a branch has no launch event), so no chat a person started
-- moves. Same launch-event rule as the view: the chat's earliest event.
UPDATE chats c
SET origin = chat_origin_of_launch_kind(le.kind)
FROM (
    SELECT DISTINCT ON (te.chat_id) te.chat_id, te.kind
    FROM trigger_events te
    WHERE te.chat_id IS NOT NULL
    ORDER BY te.chat_id, te.created_at ASC, te.id ASC
) le
WHERE le.chat_id = c.id
  AND c.origin = 1
  AND le.kind <> 'chat.start';

-- 20261030113146_chats_with_activity_index_driven's view, with origin (and
-- list_in_sidebar, which reads it) derived from the launch event when the
-- column is NULL. Every column keeps its name, position and type, so CREATE
-- OR REPLACE keeps the view, its column comments, and the generated
-- projection unchanged.
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
        COALESCE(base.origin, chat_origin_of_launch_kind(base.launch_kind)) = 1
        OR base.adopted_at IS NOT NULL
    ) AS list_in_sidebar,
    (CASE
        WHEN base.root_workflow_state IS NULL OR base.root_workflow_state = 1 THEN 1
        WHEN base.root_workflow_state = 2 THEN
            CASE base.activity WHEN 2 THEN 3 WHEN 5 THEN 8 ELSE 2 END
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
    COALESCE(base.origin, chat_origin_of_launch_kind(base.launch_kind)) AS origin
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
        (CASE
            WHEN (
                SELECT true FROM pending_input_gates g
                WHERE g.chat_id = c.id AND g.live
                LIMIT 1
            ) THEN 2
            WHEN (
                SELECT true FROM workflows w
                WHERE w.chat_id = c.id AND w.state = 2
                LIMIT 1
            ) THEN
                CASE
                    WHEN c.daemon_blocked_at IS NOT NULL THEN 5
                    WHEN ul.resume_at IS NOT NULL THEN 7
                    ELSE 1
                END
            WHEN c.queued_for_machine_at IS NOT NULL THEN 6
            WHEN (
                SELECT MAX(w.completed_at) FILTER (WHERE w.stop_reason = 2)
                     > COALESCE(MAX(w.completed_at) FILTER (WHERE w.stop_reason = 1), '-infinity'::timestamptz)
                FROM workflows w
                WHERE w.chat_id = c.id AND w.state = 3 AND w.stop_reason IN (1, 2)
            ) THEN 3
            WHEN (
                SELECT true FROM workflows w
                WHERE w.chat_id = c.id AND w.state = 3 AND w.stop_reason = 3
                LIMIT 1
            ) THEN 4
            ELSE 0
        END)::integer AS activity,
        rw.state AS root_workflow_state,
        rw.stop_reason AS root_workflow_stop_reason,
        (
            SELECT te.kind FROM trigger_events te
            WHERE te.chat_id = c.id
            ORDER BY te.created_at ASC, te.id ASC
            LIMIT 1
        ) AS launch_kind,
        (
            SELECT te.trigger_id FROM trigger_events te
            WHERE te.chat_id = c.id
            ORDER BY te.created_at ASC, te.id ASC
            LIMIT 1
        ) AS trigger_id,
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
) base;
