-- forge:backward-compatible — data only: fills worktrees.daemon_id and moves chats.active_daemon_id, both of which the previous release already reads and routes by.

-- A branch workspace (worktree) is a checkout on ONE machine's disk, recorded
-- in worktrees.daemon_id. Two kinds of rows break that:
--
-- 1. Rows from before ownership was recorded have no daemon_id, so every
--    request about them went to the user's default machine. Where the evidence
--    is unambiguous — the project is installed on exactly one of its owner's
--    machines, so every checkout of it was made there — record that machine.
--    A project installed on several machines stays unknown: guessing would put
--    the workspace on a disk that does not have it.
--
-- 2. The new-chat composer let a user pick a workspace on machine A and run
--    the chat on machine B, so the chat was pinned (active_daemon_id) to a
--    machine without its checkout: its tools, Files tab and terminal reported
--    the workspace as archived or removed. Move such chats to the machine that
--    holds their workspace, when that machine is the chat owner's. StartChat,
--    SetChatDaemon and UpdateChat now refuse the combination.
--
-- The main checkout (is_main) is on every machine with the project and has no
-- owner. A worktree being finalized by recovery is left alone: its trigger
-- refuses owner changes. No-machine chats have no pin and are untouched.
-- updated_at/last_active do not move: this is not something the user did.

UPDATE worktrees w
SET daemon_id = only_install.daemon_id
FROM (
    SELECT pd.project_id, min(pd.daemon_id) AS daemon_id
      FROM project_daemons pd
      JOIN projects p ON p.id = pd.project_id
      JOIN daemons d ON d.id = pd.daemon_id AND d.user_id = p.user_id
     WHERE pd.install_state = 'installed'
     GROUP BY pd.project_id
    HAVING count(*) = 1
) only_install
WHERE w.project_id = only_install.project_id
  AND w.daemon_id IS NULL
  AND NOT w.is_main
  AND w.recovery_finalizing_id IS NULL;

UPDATE chats c
SET active_daemon_id = w.daemon_id
FROM worktrees w
JOIN daemons d ON d.id = w.daemon_id
WHERE c.worktree_id = w.id
  AND NOT w.is_main
  AND d.user_id = c.user_id
  AND c.active_daemon_id IS NOT NULL
  AND c.active_daemon_id <> w.daemon_id
  AND NOT c.no_machine;
