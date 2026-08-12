-- +goose Up

-- The postgres `messages` and `message_content_blocks` tables were created with
-- no indexes at all beyond their primary keys on `id` — the legacy sqlite schema
-- (schema-minimal.sql) had them, but they were never carried across when the
-- postgres migrations were written. Every lookup by chat_id / thread_id /
-- context_window_id / message_id has therefore been a sequential scan over a
-- table that grows without bound for the lifetime of the install.
--
-- This is the dominant cost of the initial chat snapshot
-- (StreamingService.StreamUserUpdates → buildChatSnapshot), which fans out into
-- exactly these lookups: ListMessages by chat_id, the context-window chain walk
-- by thread_id + context_window_id, and ListContentBlocksForMessages by
-- message_id. On a large chat that path was measured at ~1.1s of server time
-- before the first byte of the snapshot reached the client.
--
-- The composite indexes lead with the equality column and carry `ordinal` as the
-- trailing key, so they serve both the filter and the ORDER BY ordinal that
-- every one of these queries applies — letting postgres skip the sort entirely
-- and, once the reads are bounded by LIMIT, stop early instead of materializing
-- the whole history.
CREATE INDEX IF NOT EXISTS idx_messages_chat_id ON messages(chat_id);
CREATE INDEX IF NOT EXISTS idx_messages_thread_ordinal ON messages(thread_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_messages_context_window_ordinal ON messages(context_window_id, ordinal);

-- GetMessageByActivityID filters on (chat_id, activity_id).
CREATE INDEX IF NOT EXISTS idx_messages_chat_activity ON messages(chat_id, activity_id);

-- ListContentBlocksForMessages selects WHERE message_id IN (...) ORDER BY
-- message_id, position — this index serves both halves.
CREATE INDEX IF NOT EXISTS idx_content_blocks_message_position ON message_content_blocks(message_id, "position");
CREATE INDEX IF NOT EXISTS idx_content_blocks_tool_call_id ON message_content_blocks(tool_call_id);
CREATE INDEX IF NOT EXISTS idx_content_blocks_activity ON message_content_blocks(activity_id, workflow_run_id);

-- The CW-chain walk resolves parent links one row at a time and lists a
-- thread's windows by sequence; both are seq scans today.
CREATE INDEX IF NOT EXISTS idx_context_windows_thread_sequence ON context_windows(thread_id, sequence);
CREATE INDEX IF NOT EXISTS idx_context_windows_parent ON context_windows(parent_context_window_id) WHERE parent_context_window_id IS NOT NULL;

-- Serves GetLatestNonMessageUpdatesPerEntity's
-- `WHERE chat_id = ? AND update_type != MESSAGE ORDER BY entity_id, sequence_number DESC`.
-- The existing idx_chat_updates_chat_seq is ordered by sequence and so can't
-- feed the DISTINCT ON without a sort; leading with (chat_id, entity_id) lets
-- postgres walk entities in order and take the first row of each.
CREATE INDEX IF NOT EXISTS idx_chat_updates_chat_entity_seq
    ON chat_updates(chat_id, entity_id, sequence_number DESC);

-- +goose Down

DROP INDEX IF EXISTS idx_messages_chat_id;
DROP INDEX IF EXISTS idx_messages_thread_ordinal;
DROP INDEX IF EXISTS idx_messages_context_window_ordinal;
DROP INDEX IF EXISTS idx_messages_chat_activity;
DROP INDEX IF EXISTS idx_content_blocks_message_position;
DROP INDEX IF EXISTS idx_content_blocks_tool_call_id;
DROP INDEX IF EXISTS idx_content_blocks_activity;
DROP INDEX IF EXISTS idx_context_windows_thread_sequence;
DROP INDEX IF EXISTS idx_context_windows_parent;
