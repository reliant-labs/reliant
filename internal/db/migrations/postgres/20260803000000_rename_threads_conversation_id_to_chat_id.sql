-- +goose Up
-- threads.conversation_id has held a chats.id value since the table was
-- created (see threads_conversation_id_fkey in
-- 20260801000000_conversation_integrity_constraints.sql, which points at
-- chats(id)) -- there has never been a conversations table. It is an
-- abandoned rename: every other FK to a chat in this schema is named
-- chat_id (messages.chat_id, tool_calls.chat_id, workflows.chat_id, ...).
-- Renaming the column is a pure rename (Postgres preserves data, indexes,
-- and default), but the FK constraint carries the old name in its
-- identifier and must be dropped and recreated to match.
ALTER TABLE threads RENAME COLUMN conversation_id TO chat_id;

ALTER TABLE threads DROP CONSTRAINT threads_conversation_id_fkey;
ALTER TABLE threads
    ADD CONSTRAINT threads_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE threads DROP CONSTRAINT threads_chat_id_fkey;
ALTER TABLE threads RENAME COLUMN chat_id TO conversation_id;
ALTER TABLE threads
    ADD CONSTRAINT threads_conversation_id_fkey
    FOREIGN KEY (conversation_id) REFERENCES chats(id) ON DELETE CASCADE;
