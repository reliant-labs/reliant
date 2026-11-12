-- forge:backward-compatible — adds nullable columns (NULL is today's private), NOT VALID checks every existing row (all NULL) satisfies, and two columns appended at the END of chats_with_activity; the previous release never names them, its inserts store NULL, and every view column it reads keeps its name, position and type.

-- Migration: org_sharing_visibility
-- Created: 2026-11-12T16:21:46Z
--
-- Read-only sharing (control-plane docs/design/org-sharing.md §2, §9; owner
-- decision 2026-11-11). A project, chat or saved workflow is shared with the
-- live members of its org when its owner sets visibility = 'org'. NULL is
-- private, which is every row today and every row an old binary writes.
--
-- visibility is a closed text enum, not a boolean, so a later 'members'
-- (explicit share rows) is an additive widening of the CHECK, not a rename.
-- A shared row must belong to an org: the grantee is "live members of the
-- row's org", which a row with no org does not have.
--
-- shared_at is when the current share was made (NULL while private); the
-- shared list orders and labels by it.
--
-- Nothing reads these columns but internal/store and internal/authz. An old
-- binary never reads them, so after a rollback every row is private to it and
-- viewers are refused (fail closed); the flag stays dormant in the row and a
-- re-upgrade re-exposes it.
--
-- The CHECKs are added NOT VALID, so the ACCESS EXCLUSIVE lock covers only the
-- catalog change; 20261112162151_org_sharing_visibility_validate validates
-- them under SHARE UPDATE EXCLUSIVE. lock_timeout bounds the wait behind a long
-- transaction: the migration fails after 5s and the next deploy retries it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE projects
    ADD COLUMN visibility text,
    ADD COLUMN shared_at timestamp with time zone,
    ADD CONSTRAINT projects_visibility_valid
        CHECK (visibility IS NULL OR (visibility = 'org' AND org_id IS NOT NULL)) NOT VALID;
COMMENT ON COLUMN projects.visibility IS
    'Who besides the owner may read the project: NULL private (the owner alone), ''org'' every live member of org_id, read-only. Set only by the owner; an org admin may clear it.';
COMMENT ON COLUMN projects.shared_at IS
    'When visibility was last set to ''org''; NULL while private.';

ALTER TABLE chats
    ADD COLUMN visibility text,
    ADD COLUMN shared_at timestamp with time zone,
    ADD CONSTRAINT chats_visibility_valid
        CHECK (visibility IS NULL OR (visibility = 'org' AND org_id IS NOT NULL)) NOT VALID;
COMMENT ON COLUMN chats.visibility IS
    'Who besides the owner may read the chat: NULL private (the owner alone), ''org'' every live member of org_id, read-only. Set only by the owner; an org admin may clear it.';
COMMENT ON COLUMN chats.shared_at IS
    'When visibility was last set to ''org''; NULL while private.';

ALTER TABLE workflow_drafts
    ADD COLUMN visibility text,
    ADD COLUMN shared_at timestamp with time zone,
    ADD CONSTRAINT workflow_drafts_visibility_valid
        CHECK (visibility IS NULL OR (visibility = 'org' AND org_id IS NOT NULL)) NOT VALID;
COMMENT ON COLUMN workflow_drafts.visibility IS
    'Who besides the owner may read the saved workflow: NULL private (the owner alone), ''org'' every live member of org_id, read-only. Set only by the owner; an org admin may clear it.';
COMMENT ON COLUMN workflow_drafts.shared_at IS
    'When visibility was last set to ''org''; NULL while private.';

-- 20261110021244_org_id_projects_chats's view with chats.visibility and
-- chats.shared_at appended as the LAST columns. CREATE OR REPLACE VIEW may
-- only add columns at the end, so every existing column keeps its name,
-- position and type, and the previous release's reads are unchanged. (CREATE
-- OR REPLACE keeps the view's and its columns' comments.)
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
    base.org_id,
    base.visibility,
    base.shared_at
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
        c.org_id,
        c.visibility,
        c.shared_at
    FROM chats c
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    LEFT JOIN provider_backoff ul ON ul.thread_id = c.workflow_id
        AND ul.waiting_since IS NOT NULL
        AND ul.reason IN ('usage_limit', 'usage_limit_probe')
) base;
