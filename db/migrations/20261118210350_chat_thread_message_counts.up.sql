-- forge:backward-compatible — a new table that triggers on messages keep; the previous release never reads it, and its message writes keep it exact through the same triggers.

-- Migration: chat_thread_message_counts
-- Created: 2026-11-18T21:03:50Z
--
-- Every ListMessages page and every chat snapshot reports how many messages
-- the chat holds. They counted them: `count(*) FROM messages WHERE chat_id =
-- $1 [AND thread_id <> $2]`, an index-only scan of idx_messages_chat_thread
-- but still one index entry per message, so O(chat). On a chat of ~1M seq it
-- was 9.9s (ListMessages) and 11.3s (snapshot) under load in prod
-- (2026-11-18 12:00Z).
--
-- chat_thread_message_counts holds the count per (chat_id, thread_id) of
-- messages, keyed by the message row's own chat_id and thread_id, so a sum
-- over it is exactly that count(*):
--
--   chat total          = sum(message_count) WHERE chat_id = $1
--   outside one thread  = the same sum without that thread's row
--
-- It is kept by triggers on messages, not by the application, so every
-- writer keeps it exact, in the transaction of its own write: every release's
-- (the previous one included), the atomic save, InsertMessage(Once), the
-- deletes of one message, of a project or of an account, the cascade from a
-- deleted chat, and SQL nobody has written yet. A row exists exactly when its
-- count is above zero: the delete trigger removes a row it brings to zero, so
-- a deleted thread or chat leaves nothing behind and the table needs no
-- foreign key (one would also make this migration lock chats).
--
-- Write cost: one upsert of one small row per inserting statement, on the
-- (chat, thread) whose ordinal counter (message_order_counters) the same
-- write already row-locks, so it serializes nothing that was not already
-- serialized. The triggers are per statement over transition tables, so a
-- bulk write is one grouped upsert, not one per row.
--
-- The backfill below is exact without stopping anything for long: CREATE
-- TRIGGER takes SHARE ROW EXCLUSIVE on messages, held to this transaction's
-- commit, so every message write in flight committed before the backfill
-- reads and every later one waits for the triggers. Message writes therefore
-- pause for the backfill itself, one index-only pass over
-- idx_messages_chat_thread (prod: 1.7M messages, a 25 MB index, 66k threads;
-- about a second). lock_timeout bounds the wait behind a long transaction:
-- the migration fails after 5s and the next deploy retries it.
SET LOCAL lock_timeout = '5s';

CREATE TABLE chat_thread_message_counts (
    chat_id text NOT NULL,
    thread_id text NOT NULL,
    message_count bigint NOT NULL,
    PRIMARY KEY (chat_id, thread_id)
);

-- Written only by the triggers below; read through
-- internal/store/message_repo_ext.go. No store entity, so no generated
-- writer can drift it.
COMMENT ON TABLE chat_thread_message_counts IS 'forge:no-entity';

CREATE FUNCTION chat_thread_message_counts_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- Ordered, so two statements that each add to several threads take the
  -- rows' locks in one order and cannot deadlock on them.
  INSERT INTO chat_thread_message_counts AS c (chat_id, thread_id, message_count)
  SELECT chat_id, thread_id, count(*)
  FROM inserted_messages
  GROUP BY chat_id, thread_id
  ORDER BY chat_id, thread_id
  ON CONFLICT (chat_id, thread_id) DO UPDATE
  SET message_count = c.message_count + EXCLUDED.message_count;
  RETURN NULL;
END $$;

CREATE FUNCTION chat_thread_message_counts_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE chat_thread_message_counts c
  SET message_count = c.message_count - d.n
  FROM (
    SELECT chat_id, thread_id, count(*) AS n
    FROM deleted_messages
    GROUP BY chat_id, thread_id
  ) d
  WHERE c.chat_id = d.chat_id AND c.thread_id = d.thread_id;

  -- A row exists exactly when its count is above zero.
  DELETE FROM chat_thread_message_counts c
  USING (SELECT DISTINCT chat_id, thread_id FROM deleted_messages) d
  WHERE c.chat_id = d.chat_id AND c.thread_id = d.thread_id AND c.message_count <= 0;
  RETURN NULL;
END $$;

-- Nothing moves a message to another chat or thread today; if something
-- ever does, the counts follow it.
CREATE FUNCTION chat_thread_message_counts_move() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE chat_thread_message_counts
  SET message_count = message_count - 1
  WHERE chat_id = OLD.chat_id AND thread_id = OLD.thread_id;
  DELETE FROM chat_thread_message_counts
  WHERE chat_id = OLD.chat_id AND thread_id = OLD.thread_id AND message_count <= 0;

  INSERT INTO chat_thread_message_counts AS c (chat_id, thread_id, message_count)
  VALUES (NEW.chat_id, NEW.thread_id, 1)
  ON CONFLICT (chat_id, thread_id) DO UPDATE
  SET message_count = c.message_count + 1;
  RETURN NULL;
END $$;

CREATE FUNCTION chat_thread_message_counts_truncate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM chat_thread_message_counts;
  RETURN NULL;
END $$;

CREATE TRIGGER chat_thread_message_counts_insert AFTER INSERT ON messages
  REFERENCING NEW TABLE AS inserted_messages
  FOR EACH STATEMENT EXECUTE FUNCTION chat_thread_message_counts_insert();
CREATE TRIGGER chat_thread_message_counts_delete AFTER DELETE ON messages
  REFERENCING OLD TABLE AS deleted_messages
  FOR EACH STATEMENT EXECUTE FUNCTION chat_thread_message_counts_delete();
CREATE TRIGGER chat_thread_message_counts_move AFTER UPDATE OF chat_id, thread_id ON messages
  FOR EACH ROW
  WHEN (OLD.chat_id IS DISTINCT FROM NEW.chat_id OR OLD.thread_id IS DISTINCT FROM NEW.thread_id)
  EXECUTE FUNCTION chat_thread_message_counts_move();
CREATE TRIGGER chat_thread_message_counts_truncate AFTER TRUNCATE ON messages
  FOR EACH STATEMENT EXECUTE FUNCTION chat_thread_message_counts_truncate();

INSERT INTO chat_thread_message_counts (chat_id, thread_id, message_count)
SELECT chat_id, thread_id, count(*)
FROM messages
GROUP BY chat_id, thread_id;
