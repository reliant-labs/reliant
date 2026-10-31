-- forge:backward-compatible — serialize workflow admission against every chat and thread workspace without changing schema or signatures
-- Migration: workflow_thread_worktree_admission
-- Created: 2026-10-31T04:02:20Z
--
-- The schema is the 78 migration(s) in this directory, applied in order (newest: 20261031014440_drop_run_finished_inbox_dismissals.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- REPLACES 20261027140617_workflow_thread_worktree_admission (reliant#1970).
-- That file merged (#1666) at a version below prod's applied head
-- (20261029064544), so forge's migrator refused to boot on it: a version at
-- or below the schema's can never run. This is the same SQL at a version
-- above main's newest, written to be re-runnable: a database that already
-- ran 20261027140617 (dev and test databases created from main since the
-- merge) runs it as a no-op, and prod runs it for the first time. The
-- orphaned 20261027140617 record those databases keep is harmless: the
-- migrator judges only the versions this binary embeds
-- (forge/pkg/migratekit/missing.go).
--
-- Chat rows serialize changes to the binding set; then workspace rows are
-- locked in stable order. Match WorktreeRunActivities' conservative scope:
-- every chat binding AND every spawned-thread override, including paused runs.
CREATE OR REPLACE FUNCTION recovery_workflow_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE parent_id text; oldchat text; target record;
BEGIN
 IF NEW.state IN (1,2) OR (NEW.state=3 AND NEW.stop_reason=3) THEN
   IF TG_OP='UPDATE' THEN oldchat := OLD.chat_id; END IF;
   FOR parent_id IN SELECT id FROM chats WHERE id IN (NEW.chat_id,oldchat)
       ORDER BY id FOR NO KEY UPDATE LOOP
   END LOOP;
   FOR target IN SELECT id,recovery_finalizing_id FROM worktrees WHERE id IN (
       SELECT worktree_id FROM chats WHERE id IN (NEW.chat_id,oldchat)
       UNION SELECT worktree_id FROM threads WHERE chat_id IN (NEW.chat_id,oldchat)
     ) ORDER BY id FOR NO KEY UPDATE LOOP
     IF target.recovery_finalizing_id IS NOT NULL THEN
       RAISE EXCEPTION 'worktree is finalizing recovery' USING ERRCODE='55P03';
     END IF;
     UPDATE worktrees SET activity_generation=activity_generation+1 WHERE id=target.id;
   END LOOP;
 END IF;
 RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS recovery_workflow_admission ON workflows;
CREATE TRIGGER recovery_workflow_admission BEFORE INSERT OR UPDATE OF state,stop_reason,chat_id,thread ON workflows
 FOR EACH ROW EXECUTE FUNCTION recovery_workflow_admission();

-- A new/rebound override must also serialize with an already-running parent
-- workflow and with archival of either endpoint. Otherwise a concurrent
-- binding can be absent from the workflow trigger's snapshot.
CREATE OR REPLACE FUNCTION recovery_thread_binding_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE parent_id text; oldchat text; oldwt text; target record;
BEGIN
 IF TG_OP='UPDATE' THEN oldchat:=OLD.chat_id; oldwt:=OLD.worktree_id; END IF;
 FOR parent_id IN SELECT id FROM chats WHERE id IN (NEW.chat_id,oldchat)
     ORDER BY id FOR NO KEY UPDATE LOOP
 END LOOP;
 FOR target IN SELECT id,recovery_finalizing_id FROM worktrees WHERE id IN (
     SELECT worktree_id FROM chats WHERE id IN (NEW.chat_id,oldchat)
     UNION SELECT NEW.worktree_id UNION SELECT oldwt
   ) ORDER BY id FOR NO KEY UPDATE LOOP
   IF target.recovery_finalizing_id IS NOT NULL THEN
     RAISE EXCEPTION 'worktree is finalizing recovery' USING ERRCODE='55P03';
   END IF;
   UPDATE worktrees SET activity_generation=activity_generation+1 WHERE id=target.id;
 END LOOP;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS recovery_thread_binding_admission ON threads;
CREATE TRIGGER recovery_thread_binding_admission BEFORE INSERT OR UPDATE OF worktree_id,chat_id ON threads
 FOR EACH ROW EXECUTE FUNCTION recovery_thread_binding_admission();
