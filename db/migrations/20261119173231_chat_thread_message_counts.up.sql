-- forge:backward-compatible — two new tables that triggers on messages keep; the previous release never reads them, and its message writes keep them exact through the same triggers.
-- forge:statement-timeout 10s

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
-- chat_thread_message_counts holds a count per (chat_id, thread_id), keyed by
-- the message row's own chat_id and thread_id. Triggers on messages move it
-- in the transaction of every message write, so every writer keeps it: every
-- release's (the previous one included), the atomic save,
-- InsertMessage(Once), the deletes of one message, of a project or of an
-- account, the cascade from a deleted chat, and SQL nobody has written yet.
--
-- WHAT A ROW MEANS. For a chat that is not in chat_message_counts_pending, a
-- row is exactly that thread's count(*) (a missing row is zero, and a row
-- that reaches zero is removed), so its sum is the chat's total:
--
--   chat total          = sum(message_count) WHERE chat_id = $1
--   outside one thread  = the same sum without that thread's row
--
-- For a chat in chat_message_counts_pending — every chat that exists when
-- this migration runs — a row is only the change since the triggers were
-- installed (it may be negative: a deleted older message). Such a chat is
-- counted with count(*), as before, until its first read initializes it
-- (store.initChatMessageCounts): one statement that deletes its pending row
-- and adds to each thread's row the count(*) less the change, both read from
-- that statement's one snapshot. The difference is the thread's count when
-- the triggers were installed, a constant, so it is right whatever is written
-- concurrently, and the pending row makes it happen once.
--
-- NO SCAN UNDER A LOCK. CREATE TRIGGER takes SHARE ROW EXCLUSIVE on messages,
-- which pauses message writes until this transaction commits. Nothing here
-- reads messages: the only scan is chats' ids (prod: 46k rows), so the pause
-- is the DDL and that insert. The pending set must be read AFTER the triggers
-- exist: a chat committed later has every one of its messages counted by
-- them, so it needs no initialization. The timeout directives bound the rest:
-- a lock not had in 5s (the migrator's default) is retried, a statement over
-- 10s fails the migration with the schema left as it was, and the next run
-- retries it.
--
-- Write cost: one upsert of one small row per writing statement, on the
-- (chat, thread) whose ordinal counter (message_order_counters) an inserting
-- write already row-locks, so it serializes nothing that was not already
-- serialized. The triggers are per statement over transition tables, so a
-- bulk write is one grouped upsert per thread, not one per row. Neither table
-- has a foreign key: one to chats would make this migration lock chats too.

CREATE TABLE chat_thread_message_counts (
    chat_id text NOT NULL,
    thread_id text NOT NULL,
    message_count bigint NOT NULL,
    PRIMARY KEY (chat_id, thread_id)
);

CREATE TABLE chat_message_counts_pending (
    chat_id text PRIMARY KEY
);

-- Written only by the triggers below and by store.initChatMessageCounts; read
-- through internal/store/message_repo_ext.go. No store entity, so no
-- generated writer can drift them.
COMMENT ON TABLE chat_thread_message_counts IS 'forge:no-entity';
COMMENT ON TABLE chat_message_counts_pending IS 'forge:no-entity';

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
  -- An upsert, not an update: a pending chat's older messages have no row
  -- until something changes them, and their removal is a negative change.
  INSERT INTO chat_thread_message_counts AS c (chat_id, thread_id, message_count)
  SELECT chat_id, thread_id, -count(*)
  FROM deleted_messages
  GROUP BY chat_id, thread_id
  ORDER BY chat_id, thread_id
  ON CONFLICT (chat_id, thread_id) DO UPDATE
  SET message_count = c.message_count + EXCLUDED.message_count;

  -- A zero row says what no row says. (A negative one does not: it is a
  -- pending chat's change.)
  DELETE FROM chat_thread_message_counts c
  USING (SELECT DISTINCT chat_id, thread_id FROM deleted_messages) d
  WHERE c.chat_id = d.chat_id AND c.thread_id = d.thread_id AND c.message_count = 0;
  RETURN NULL;
END $$;

-- Nothing moves a message to another chat or thread today; if something
-- ever does, the counts follow it.
CREATE FUNCTION chat_thread_message_counts_move() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO chat_thread_message_counts AS c (chat_id, thread_id, message_count)
  VALUES (OLD.chat_id, OLD.thread_id, -1)
  ON CONFLICT (chat_id, thread_id) DO UPDATE
  SET message_count = c.message_count - 1;
  DELETE FROM chat_thread_message_counts
  WHERE chat_id = OLD.chat_id AND thread_id = OLD.thread_id AND message_count = 0;

  INSERT INTO chat_thread_message_counts AS c (chat_id, thread_id, message_count)
  VALUES (NEW.chat_id, NEW.thread_id, 1)
  ON CONFLICT (chat_id, thread_id) DO UPDATE
  SET message_count = c.message_count + 1;
  DELETE FROM chat_thread_message_counts
  WHERE chat_id = NEW.chat_id AND thread_id = NEW.thread_id AND message_count = 0;
  RETURN NULL;
END $$;

-- Emptying messages leaves every chat with zero messages: no rows, and
-- nothing left to initialize.
CREATE FUNCTION chat_thread_message_counts_reset() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM chat_thread_message_counts;
  DELETE FROM chat_message_counts_pending;
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
CREATE TRIGGER chat_thread_message_counts_reset AFTER TRUNCATE ON messages
  FOR EACH STATEMENT EXECUTE FUNCTION chat_thread_message_counts_reset();

-- Every chat that may hold a message written before the triggers existed.
INSERT INTO chat_message_counts_pending (chat_id)
SELECT id FROM chats;
