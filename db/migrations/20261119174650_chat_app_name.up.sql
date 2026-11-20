-- forge:backward-compatible — adds a nullable column (NULL is "about no app", every row today and every row an old binary writes), a NOT VALID check every existing row (all NULL) satisfies, and app_name appended at the END of chats_with_activity; the previous release never names it, and every view column it reads keeps its name, position and type.

-- Migration: chat_app_name
-- Created: 2026-11-19T17:46:50Z
--
-- A chat's app tag (control-plane docs/design/unified-shell-and-projects.md
-- §8.5, D4): the forge.yaml `name` of the app a chat is about, in the chat's
-- org. StartChat stores it when a chat starts from an app (its page, Ask
-- Reliant, an alert) and nothing infers one, so NULL is a chat about no
-- particular app. It is written once, at creation, and a branch copies it.
--
-- The CHECK holds forge's project-name shape (forge
-- internal/cli/cmdutil.ValidateProjectName: a letter first, then letters,
-- digits, '_' or '-') for every ASCII character, plus reliant's 128-character
-- bound. forge's letters and digits are Unicode ones, which a POSIX class
-- judges by the database's locale, so the CHECK admits any non-ASCII
-- character and the server (StartChat) refuses non-letters among them: the
-- CHECK never refuses a name the server accepted.
--
-- The CHECK is added NOT VALID, so the ACCESS EXCLUSIVE lock covers only the
-- catalog change; 20261119174722_chat_app_name_validate validates it under
-- SHARE UPDATE EXCLUSIVE. lock_timeout bounds the wait behind a long
-- transaction: the migration fails after 5s and the next deploy retries it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE chats
    ADD COLUMN app_name text,
    ADD CONSTRAINT chats_app_name_valid
        CHECK (app_name IS NULL OR (
            char_length(app_name) <= 128
            AND app_name ~ '^[A-Za-z\u0080-\U0010FFFF][A-Za-z0-9_\u0080-\U0010FFFF-]*$'
        )) NOT VALID;
COMMENT ON COLUMN chats.app_name IS
    'The app the chat is about: a forge.yaml name in the chat''s org, set at creation when the chat was started from that app; NULL for a chat about no particular app. Never inferred.';

-- 20261112162146_org_sharing_visibility's view with chats.app_name appended
-- as the LAST column. CREATE OR REPLACE VIEW may only add columns at the end,
-- so every existing column keeps its name, position and type, and the
-- previous release's reads are unchanged. (CREATE OR REPLACE keeps the view's
-- and its columns' comments.)
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
    base.shared_at,
    base.app_name
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
        c.shared_at,
        c.app_name
    FROM chats c
    LEFT JOIN workflows rw ON rw.id = c.workflow_id
    LEFT JOIN provider_backoff ul ON ul.thread_id = c.workflow_id
        AND ul.waiting_since IS NOT NULL
        AND ul.reason IN ('usage_limit', 'usage_limit_probe')
) base;
