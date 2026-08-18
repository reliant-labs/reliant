-- +goose Up
-- Give each durable update stream its own transactional sequence allocator.
--
-- A table-global allocator makes ordinary writes to OTHER users/chats appear
-- as gaps in a scoped stream. That contradicts the streaming client's cursor
-- contract: user_updates is resumed within one user and chat_updates within
-- one chat, and a jump is treated as evidence that delivery was lost.
--
-- This row is deliberately updated in the SAME transaction as its ledger row.
-- The update locks one logical stream until commit. A concurrent writer for
-- that same stream therefore cannot obtain N+1 until the writer of N commits;
-- if N rolls back, its counter update rolls back as well. Writers for unrelated
-- streams touch different rows and do not contend.
CREATE TABLE update_stream_counters (
    stream_kind text NOT NULL,
    stream_id text NOT NULL,
    last_assigned_seq bigint NOT NULL,
    CONSTRAINT update_stream_counters_pkey
        PRIMARY KEY (stream_kind, stream_id),
    CONSTRAINT update_stream_counters_kind_check
        CHECK (stream_kind IN ('user', 'chat')),
    CONSTRAINT update_stream_counters_positive_check
        CHECK (last_assigned_seq >= 1)
);

-- Preserve every existing cursor exactly. Historical values may be sparse
-- because older allocators were table-global; the next scoped allocation starts
-- at that stream's own high-water mark plus one.
INSERT INTO update_stream_counters (stream_kind, stream_id, last_assigned_seq)
SELECT 'user', user_id, MAX(sequence_number)
FROM user_updates
GROUP BY user_id;

INSERT INTO update_stream_counters (stream_kind, stream_id, last_assigned_seq)
SELECT 'chat', chat_id, MAX(sequence_number)
FROM chat_updates
GROUP BY chat_id;

-- Remove the superseded allocators. This is an intentional clean cutover: no
-- old binary may continue writing after this migration is applied.
DROP SEQUENCE IF EXISTS user_updates_sequence_number_seq;
DROP SEQUENCE IF EXISTS chat_updates_sequence_number_seq;

-- +goose Down
DROP TABLE update_stream_counters;
