-- +goose Up

-- chat_updates was created without the (chat_id, sequence_number) index the
-- legacy sqlite schema had (schema-minimal.sql). Without it, CreateChatUpdate's
-- MAX(sequence_number) read seq-scans the table, and under SERIALIZABLE
-- isolation a seq scan takes a relation-wide SIREAD predicate lock — so every
-- concurrent chat_updates insert conflicts with every other ACROSS chats,
-- producing SQLSTATE 40001 abort storms that exhaust runTxWithRetries whenever
-- several chats stream at once. This index confines predicate locks to
-- per-chat index ranges and makes the MAX read an index lookup.
--
-- Deliberately a plain index, not UNIQUE(chat_id, sequence_number) like the
-- sqlite schema: uniqueness is already guaranteed dynamically by the
-- SERIALIZABLE write path (all CreateChatUpdate writes go through RunTx), and
-- a plain index cannot fail this migration on an existing database the way a
-- UNIQUE build would if legacy rows ever contained a duplicate.
CREATE INDEX IF NOT EXISTS idx_chat_updates_chat_seq ON chat_updates(chat_id, sequence_number DESC);
CREATE INDEX IF NOT EXISTS idx_chat_updates_created ON chat_updates(created_at DESC);

-- +goose Down

DROP INDEX IF EXISTS idx_chat_updates_chat_seq;
DROP INDEX IF EXISTS idx_chat_updates_created;
