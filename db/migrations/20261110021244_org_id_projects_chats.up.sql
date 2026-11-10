-- forge:backward-compatible — adds nullable columns, a NOT VALID check every existing row (all NULL) satisfies, and one column appended at the END of chats_with_activity; the previous release never names org_id, its inserts store NULL, and every view column it reads keeps its name, position and type.

-- Migration: org_id_projects_chats
-- Created: 2026-11-10T02:12:44Z
--
-- Every project and every chat belongs to exactly one organization
-- (control-plane docs/design/orgs-and-members.md §3, owner decision
-- 2026-11-09; §5, phase 4). The org is control-plane's: reliant stores its id
-- and never a copy of membership, so there is no foreign key.
--
-- NULL is every row written before this release, and any row a creating
-- request could not resolve an org for (no control plane, or it could not be
-- reached). It means what every row meant before: visible to its user_id only.
-- The api-server stamps a user's NULL rows with their default org the first
-- time that user lists by org (internal/orgs, lazy backfill), so no migration
-- reads control-plane.
--
-- The CHECK keeps '' out: an org is named or it is NULL, never blank. It is
-- added NOT VALID so the ACCESS EXCLUSIVE lock covers only the catalog change;
-- 20261110021253_org_id_projects_chats_validate validates it under SHARE
-- UPDATE EXCLUSIVE. lock_timeout bounds the wait behind a long transaction:
-- the migration fails after 5s and the next deploy retries it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE projects
    ADD COLUMN org_id text,
    ADD CONSTRAINT projects_org_id_not_blank CHECK (org_id <> '') NOT VALID;
COMMENT ON COLUMN projects.org_id IS
    'The organization (control-plane organizations.id) the project belongs to, fixed at creation. NULL: written before orgs, or no org could be resolved; visible to user_id alone.';

ALTER TABLE chats
    ADD COLUMN org_id text,
    ADD CONSTRAINT chats_org_id_not_blank CHECK (org_id <> '') NOT VALID;
COMMENT ON COLUMN chats.org_id IS
    'The organization (control-plane organizations.id) the chat belongs to, fixed at creation; its LLM usage bills this org. NULL: written before orgs, or no org could be resolved; visible to user_id alone.';

-- 20261102040920_chat_origin_fails_closed's view with chats.org_id appended as
-- the LAST column. CREATE OR REPLACE VIEW may only add columns at the end, so
-- every existing column keeps its name, position and type, and the previous
-- release's reads are unchanged.
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
    COALESCE(base.origin, chat_origin_of_launch_kind(base.launch_kind)) AS origin,
    base.org_id
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
        c.origin,
        c.org_id
    FROM chats c
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    LEFT JOIN provider_backoff ul ON ul.thread_id = c.workflow_id
        AND ul.waiting_since IS NOT NULL
        AND ul.reason IN ('usage_limit', 'usage_limit_probe')
) base;
