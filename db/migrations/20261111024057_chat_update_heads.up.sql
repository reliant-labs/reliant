-- forge:backward-compatible — a new table and a column with a constant default; the previous release reads neither, and its update_stream_counters upserts name their columns, so the default fills the new one.

-- Migration: chat_update_heads
--
-- The chat snapshot's update heads, materialized (dev-docs/design/
-- chat-update-heads.md). Every snapshot used to compute them from the whole
-- log (store.SnapshotHeadsSQL, a skip scan over chat_updates: 11GB on prod, a
-- cold read per head, up to 9.5s). This table holds the newest row per head
-- key of each chat, written in the same transaction as the log row by
-- store.ApplyChatUpdateHeads, so a snapshot reads one primary-key range.
--
-- head_key is the identity SnapshotHeadsSQL dedups on, in two namespaces that
-- mirror its two halves: 'e:' || entity_id for the entity-keyed types, and
-- 'r:' || SnapshotRekeyKeySQL for THREAD (3) and QUESTION (18). update_id is
-- the head's chat_updates.id. No foreign key to chats, like chat_updates: a
-- head write must never fail where the log insert would succeed.
CREATE TABLE chat_update_heads (
    chat_id text NOT NULL,
    head_key text NOT NULL,
    update_id text NOT NULL,
    sequence_number bigint NOT NULL,
    update_type integer NOT NULL,
    entity_id text NOT NULL,
    data text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    PRIMARY KEY (chat_id, head_key)
);
-- Written only by the store's upsert, which keeps the highest sequence per key;
-- no generated CRUD may bypass it.
COMMENT ON TABLE chat_update_heads IS 'forge:no-entity';

-- How far a chat's heads are known complete. A release that maintains the
-- heads advances it in the same upsert that allocates the sequence, but only
-- while it equals last_assigned_seq; the previous release allocates without
-- touching it, so its first write leaves the chat behind for good and the
-- snapshot reads the log (as before) until the backfill catches it up.
-- Meaningful for stream_kind 'chat' only. No index: the allocation stays a
-- HOT update.
ALTER TABLE update_stream_counters
    ADD COLUMN heads_through_seq bigint NOT NULL DEFAULT 0;
