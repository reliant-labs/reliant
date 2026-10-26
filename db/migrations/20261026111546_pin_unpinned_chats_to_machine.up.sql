-- forge:backward-compatible — data only: sets chats.active_daemon_id, which the previous release already reads and honours as a pin.

-- Chats started with the default machine stored no daemon, so every tool call
-- re-ran default resolution, which prefers a connected local machine: a user
-- with a cloud machine and several chats who connected their laptop saw every
-- chat move to it (research/CHAT_MACHINE_PLACEMENT.md). New runs now pin the
-- chat before they start; this pins the existing ones from evidence only:
--
--   a. the machine holding the chat's worktree checkout, else
--   b. the project's only installed machine,
--
-- and only a machine the chat's own user owns. A chat with neither (no
-- worktree owner, the project installed on none or on several machines) stays
-- unpinned and is pinned by its next run. No-machine chats are untouched (the
-- chats_no_machine_has_no_daemon_check constraint), and updated_at/last_active
-- do not move: this is not something the user did to the chat.
UPDATE chats
SET active_daemon_id = evidence.daemon_id
FROM (
    SELECT c.id AS chat_id,
           COALESCE(
               (SELECT d.id
                  FROM worktrees w
                  JOIN daemons d ON d.id = w.daemon_id AND d.user_id = c.user_id
                 WHERE w.id = c.worktree_id),
               (SELECT d.id
                  FROM project_daemons pd
                  JOIN daemons d ON d.id = pd.daemon_id AND d.user_id = c.user_id
                 WHERE pd.project_id = c.project_id
                   AND pd.install_state = 'installed'
                   AND (SELECT count(*) FROM project_daemons x
                         WHERE x.project_id = c.project_id AND x.install_state = 'installed') = 1)
           ) AS daemon_id
      FROM chats c
     WHERE c.active_daemon_id IS NULL AND NOT c.no_machine
) evidence
WHERE chats.id = evidence.chat_id AND evidence.daemon_id IS NOT NULL;
