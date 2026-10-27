-- forge:backward-compatible — serialize workflow admission against every chat and thread workspace without changing schema or signatures
-- Migration: workflow_thread_worktree_admission
-- Created: 2026-10-27T14:06:17Z
--
-- The schema is the 44 migration(s) in this directory, applied in order (newest: 20261027011600_redact_automation_history.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
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

DROP TRIGGER recovery_workflow_admission ON workflows;
CREATE TRIGGER recovery_workflow_admission BEFORE INSERT OR UPDATE OF state,stop_reason,chat_id,thread ON workflows
 FOR EACH ROW EXECUTE FUNCTION recovery_workflow_admission();

-- A new/rebound override must also serialize with an already-running parent
-- workflow and with archival of either endpoint. Otherwise a concurrent
-- binding can be absent from the workflow trigger's snapshot.
CREATE FUNCTION recovery_thread_binding_admission() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER recovery_thread_binding_admission BEFORE INSERT OR UPDATE OF worktree_id,chat_id ON threads
 FOR EACH ROW EXECUTE FUNCTION recovery_thread_binding_admission();

