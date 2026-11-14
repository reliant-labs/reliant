-- forge:backward-compatible — a new table, function and triggers; the previous release never purges a chat through purged_chats, so the triggers refuse none of its writes until this release's DeleteUserProject has deleted the chat it writes to, and a write to a deleted chat is the orphan they exist to refuse.

-- Migration: chat_purge_write_admission
-- Created: 2026-11-14T22:15:37Z
--
-- chat_updates, chat_update_heads and the chat's update_stream_counters row
-- have no foreign key to chats (a head write must never fail where the log
-- insert would succeed: 20261111024057_chat_update_heads). So nothing stops a
-- running workflow writing to a chat that DeleteUserProject
-- (internal/store/project_delete.go) is purging: its row commits after the
-- purge's DELETE ran, the chat goes, and the row is orphaned for good.
--
-- The protocol, which holds for every release's writers because it lives
-- here rather than in any one of them:
--
--   - Every insert into the three tables (the counter upsert included: a
--     BEFORE INSERT trigger fires for INSERT ... ON CONFLICT too) takes a key
--     share on the chat's row, held until the writer commits.
--   - The purge locks its chats FOR UPDATE before deleting anything, records
--     them in purged_chats, and deletes them in that same transaction.
--
-- FOR UPDATE and FOR KEY SHARE conflict, so a writer that got there first is
-- waited for, and its rows are then visible to the purge's DELETEs; a writer
-- that comes later waits for the purge and then finds the chat gone and
-- tombstoned, and fails with a foreign_key_violation. A chat that is missing
-- and NOT tombstoned (one that never existed: fixtures, or a write racing the
-- chat's own creation in another transaction) is admitted as before.

CREATE TABLE purged_chats (
    chat_id text PRIMARY KEY,
    purged_at timestamp with time zone NOT NULL DEFAULT now()
);
-- Written only by DeleteUserProject's purge; read only by the trigger below.
COMMENT ON TABLE purged_chats IS 'forge:no-entity';

CREATE FUNCTION chat_write_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE cid text;
BEGIN
  IF TG_TABLE_NAME = 'update_stream_counters' THEN
    IF NEW.stream_kind <> 'chat' THEN
      RETURN NEW;
    END IF;
    cid := NEW.stream_id;
  ELSE
    cid := NEW.chat_id;
  END IF;
  PERFORM 1 FROM chats WHERE id = cid FOR KEY SHARE;
  IF NOT FOUND AND EXISTS (SELECT 1 FROM purged_chats WHERE chat_id = cid) THEN
    RAISE EXCEPTION 'chat % was deleted', cid USING ERRCODE = 'foreign_key_violation';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER chat_updates_write_admission BEFORE INSERT ON chat_updates
  FOR EACH ROW EXECUTE FUNCTION chat_write_admission();
CREATE TRIGGER chat_update_heads_write_admission BEFORE INSERT ON chat_update_heads
  FOR EACH ROW EXECUTE FUNCTION chat_write_admission();
CREATE TRIGGER update_stream_counters_write_admission BEFORE INSERT ON update_stream_counters
  FOR EACH ROW EXECUTE FUNCTION chat_write_admission();
