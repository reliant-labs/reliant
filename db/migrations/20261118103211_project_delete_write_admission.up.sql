-- forge:backward-compatible — a new function and triggers; the previous release never schedules a project's deletion (it never writes projects.delete_after), so the triggers refuse none of its writes until this release has deleted the project it writes to, and a new chat, workspace, run or live automation in a deleted project is exactly what they exist to refuse.

-- Migration: project_delete_write_admission
-- Created: 2026-11-18T10:32:11Z
--
-- A project scheduled for deletion (projects.delete_after,
-- 20261118040600_project_delete_after) is hidden, and its enabled triggers
-- were paused when it was scheduled. Nothing may then add to it: no new chat,
-- workspace (worktree) or run (workflow), no chat moved into it, and no
-- trigger created in it, moved into it, or turned on in it. A handler's check of the project before its write is not enough: a
-- write that passed it can commit after the deletion did, and its trigger
-- would be the enabled automation of a deleted project.
--
-- The protocol, which holds for every release's writers because it lives
-- here rather than in any one of them (the shape of chat_write_admission,
-- 20261114221537):
--
--   - Each admitted write takes a key share on its project's row, held until
--     it commits, and reads delete_after under it.
--   - Scheduling a deletion (store.ScheduleProjectDeletion) locks the project
--     row FOR UPDATE before it sets delete_after and pauses the triggers.
--
-- FOR UPDATE and FOR KEY SHARE conflict. A write that got there first is
-- waited for, so its rows are committed and visible when the deletion then
-- scans the project's triggers (and pauses the one it created or enabled). A
-- write that comes later waits for the deletion, then reads delete_after set
-- and fails, SQLSTATE 55000 with constraint project_scheduled_for_deletion
-- (store.projectDeletedError). A key share, not a share: it does not wait on
-- the project's ordinary updates (TouchProject's last_active), which take FOR
-- NO KEY UPDATE.
--
-- A restore clears delete_after first, in the same transaction that turns the
-- paused triggers back on, so those writes read it cleared.
--
-- Pausing a trigger, or any other write to a trigger left as it was, is
-- always admitted, and so is every other update of a chat, worktree or run: what is already there stays writable to
-- whatever still runs, until the purge.
--
-- Creating triggers takes SHARE ROW EXCLUSIVE on each table: lock_timeout
-- bounds the wait behind a long transaction, so the migration fails after 5s
-- and the next deploy retries it.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION project_delete_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  pid text;
  scheduled boolean;
BEGIN
  IF TG_TABLE_NAME = 'workflows' THEN
    -- A run that already exists is not a new one: its retried insert (ON
    -- CONFLICT DO NOTHING) stays the no-op it always was.
    IF EXISTS (SELECT 1 FROM workflows WHERE id = NEW.id) THEN
      RETURN NEW;
    END IF;
    SELECT c.project_id INTO pid FROM chats c WHERE c.id = NEW.chat_id;
  ELSE
    pid := NEW.project_id;
  END IF;
  IF pid IS NULL THEN
    RETURN NEW;
  END IF;
  SELECT delete_after IS NOT NULL INTO scheduled FROM projects WHERE id = pid FOR KEY SHARE;
  IF scheduled THEN
    RAISE EXCEPTION 'project % is scheduled for deletion', pid
      USING ERRCODE = 'object_not_in_prerequisite_state',
            CONSTRAINT = 'project_scheduled_for_deletion';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER project_delete_admission BEFORE INSERT ON chats
  FOR EACH ROW EXECUTE FUNCTION project_delete_admission();
CREATE TRIGGER project_delete_admission_move BEFORE UPDATE OF project_id ON chats
  FOR EACH ROW WHEN (NEW.project_id IS DISTINCT FROM OLD.project_id)
  EXECUTE FUNCTION project_delete_admission();

CREATE TRIGGER project_delete_admission BEFORE INSERT ON worktrees
  FOR EACH ROW EXECUTE FUNCTION project_delete_admission();

CREATE TRIGGER project_delete_admission BEFORE INSERT ON workflows
  FOR EACH ROW EXECUTE FUNCTION project_delete_admission();

-- A trigger is admitted when created, moved to another project, or turned on
-- (enabled, resumed, or an edit that turns it on). A write to a trigger that
-- is already on is not: a deletion pauses every trigger it finds on, so one
-- still on is in a project no deletion has scheduled yet, and the deletion
-- that comes next waits for that write and pauses it.
CREATE TRIGGER project_delete_admission BEFORE INSERT ON triggers
  FOR EACH ROW EXECUTE FUNCTION project_delete_admission();
CREATE TRIGGER project_delete_admission_update BEFORE UPDATE ON triggers
  FOR EACH ROW WHEN ((NEW.enabled AND NOT OLD.enabled) OR NEW.project_id IS DISTINCT FROM OLD.project_id)
  EXECUTE FUNCTION project_delete_admission();
