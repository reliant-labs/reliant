-- forge:backward-compatible — a new table, a replaced trigger function and four new triggers; the previous release never purges a chat through purged_chats, so the triggers refuse none of its workflow, checkpoint, question or step writes until this release's DeleteUserProject has deleted the chat they write to, and a write to a deleted chat is the orphan they exist to refuse.

-- Migration: chat_purge_workflow_admission
-- Created: 2026-11-14T22:54:15Z
--
-- 20261114221537_chat_purge_write_admission admits writes to chat_updates,
-- chat_update_heads and update_stream_counters against DeleteUserProject's
-- purge. The purge (internal/store/project_delete.go, chatPurgeSteps) also
-- deletes four more tables with no foreign key to chats: workflows,
-- workflow_checkpoints, questions and step_executions. A running workflow
-- writes all four (SaveWorkflowCheckpoint, InsertQuestion,
-- InsertWorkflowIfAbsent, InsertStepExecution), and is not cancelled by the
-- delete, so a row written after the purge's DELETE outlived its chat.
--
-- The same protocol now covers them: every insert takes a key share on its
-- chat (an upsert too: BEFORE INSERT fires for INSERT ... ON CONFLICT), and is
-- refused once the chat is gone and tombstoned.
--
-- step_executions names a workflow, not a chat, so the trigger resolves the
-- chat through the workflow row. A writer that read the workflow before the
-- purge committed then waits on the chat and is refused. One that comes after
-- finds the workflow gone, so the purge tombstones its workflows too
-- (purged_workflows), in the transaction that deletes them; a step of a
-- workflow that never existed, and was never purged, is admitted as before.
--
-- Lock order: a trigger locks the chat row first, before the insert takes any
-- lock of its own, and the purge locks the chat FOR UPDATE before it deletes
-- anything. workflows' recovery_workflow_admission locks the same chat and
-- then worktrees; triggers fire in name order, so this key share comes first
-- and the order is chat, then worktrees, as before.

CREATE TABLE purged_workflows (
    workflow_id text PRIMARY KEY,
    chat_id text NOT NULL,
    purged_at timestamp with time zone NOT NULL DEFAULT now()
);
-- Written only by DeleteUserProject's purge; read only by the trigger below.
COMMENT ON TABLE purged_workflows IS 'forge:no-entity';

CREATE OR REPLACE FUNCTION chat_write_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE cid text;
BEGIN
  IF TG_TABLE_NAME = 'update_stream_counters' THEN
    IF NEW.stream_kind <> 'chat' THEN
      RETURN NEW;
    END IF;
    cid := NEW.stream_id;
  ELSIF TG_TABLE_NAME = 'step_executions' THEN
    SELECT chat_id INTO cid FROM workflows WHERE id = NEW.workflow_id;
    IF NOT FOUND THEN
      IF EXISTS (SELECT 1 FROM purged_workflows WHERE workflow_id = NEW.workflow_id) THEN
        RAISE EXCEPTION 'workflow % was deleted', NEW.workflow_id USING ERRCODE = 'foreign_key_violation';
      END IF;
      RETURN NEW;
    END IF;
  ELSE
    cid := NEW.chat_id;
  END IF;
  PERFORM 1 FROM chats WHERE id = cid FOR KEY SHARE;
  IF NOT FOUND AND EXISTS (SELECT 1 FROM purged_chats WHERE chat_id = cid) THEN
    RAISE EXCEPTION 'chat % was deleted', cid USING ERRCODE = 'foreign_key_violation';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER chat_write_admission BEFORE INSERT ON workflows
  FOR EACH ROW EXECUTE FUNCTION chat_write_admission();
CREATE TRIGGER chat_write_admission BEFORE INSERT ON workflow_checkpoints
  FOR EACH ROW EXECUTE FUNCTION chat_write_admission();
CREATE TRIGGER chat_write_admission BEFORE INSERT ON questions
  FOR EACH ROW EXECUTE FUNCTION chat_write_admission();
CREATE TRIGGER chat_write_admission BEFORE INSERT ON step_executions
  FOR EACH ROW EXECUTE FUNCTION chat_write_admission();
