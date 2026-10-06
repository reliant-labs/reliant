-- +goose Up

-- A chat with no machine pins no daemon (research/NO_MACHINE_CHATS.md). The
-- two used to be able to disagree: SetChatDaemon set active_daemon_id and left
-- no_machine true, so a "connected" chat kept running with no machine. Pinning
-- a daemon now clears no_machine in the same write (UpdateChatActiveDaemon), and
-- this constraint makes the contradiction unrepresentable, the same rule
-- triggers_no_machine_names_no_daemon_check states for triggers.
--
-- A row that already disagrees was connected to a machine after it started, so
-- the daemon is the user's later choice and wins: the heal clears no_machine
-- rather than dropping the daemon. Re-runnable, like every migration here.
UPDATE chats SET no_machine = false WHERE no_machine AND active_daemon_id IS NOT NULL;

ALTER TABLE chats DROP CONSTRAINT IF EXISTS chats_no_machine_has_no_daemon_check;
ALTER TABLE chats ADD CONSTRAINT chats_no_machine_has_no_daemon_check
    CHECK (NOT no_machine OR active_daemon_id IS NULL);
