-- forge:backward-compatible — data only: moves chats.active_daemon_id, which the previous release already reads and routes by.

-- A branch workspace (worktree) is a checkout on ONE machine's disk, recorded
-- in worktrees.daemon_id. The new-chat composer let a user pick a workspace on
-- machine A and run the chat on machine B, so the chat was pinned
-- (active_daemon_id) to a machine without its checkout: its tools, Files tab
-- and terminal reported the workspace as archived or removed. Move such chats
-- to the machine that holds their workspace, when that machine is the chat
-- owner's. StartChat, SetChatDaemon and UpdateChat now refuse the combination.
--
-- Rows with no daemon_id (from before ownership was recorded) are left alone.
-- Nothing in the database proves which disk holds one: an install row can be
-- removed while the workspace survives on that machine, and a branch
-- directory is separate from the project's clone. They are settled from disk
-- instead, by daemonplacement.Resolver on first use and by the worktree sweep.
--
-- The main checkout (is_main) is on every machine with the project and has no
-- owner. No-machine chats have no pin and are untouched. updated_at and
-- last_active do not move: this is not something the user did.

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
