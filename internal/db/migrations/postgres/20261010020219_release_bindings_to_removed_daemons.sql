-- +goose Up
-- Release every binding to a machine the registry no longer has.
--
-- Removing a daemon (a control-plane delete, or a registry snapshot that no
-- longer lists it) now releases what named it in the same transaction:
-- chats.active_daemon_id, worktrees.daemon_id and project_daemons
-- (db.Repo.releaseDaemonBindings). Rows already left dangling by removals that
-- happened before that are released here, the same way. Each was a dead end:
-- a chat pinned to a daemon that no longer exists routed its tools there
-- forever, a workspace owned by one routed its files there, and an install on
-- one made validateOwnedProjectDaemon refuse every OTHER machine for the
-- project. Released, the chat falls to default resolution, the workspace is
-- adopted by the machine that finds its directory, and the install is
-- re-recorded by the machine that has the checkout when it connects.
--
-- Prod on 2026-10-10: 0 chat pins, 1 live worktree owner and 13 project
-- installs pointed at removed daemons.
--
-- A pin was only ever set to a daemon with a registry row (StartChat and
-- SetChatDaemon validate it), so a missing row means the machine was removed.
UPDATE chats SET active_daemon_id = NULL, updated_at = NOW()
WHERE active_daemon_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM daemons d WHERE d.id = chats.active_daemon_id);

UPDATE worktrees SET daemon_id = NULL
WHERE daemon_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM daemons d WHERE d.id = worktrees.daemon_id);

DELETE FROM project_daemons
WHERE NOT EXISTS (SELECT 1 FROM daemons d WHERE d.id = project_daemons.daemon_id);
