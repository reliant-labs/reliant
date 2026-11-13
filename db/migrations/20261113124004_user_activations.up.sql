-- forge:backward-compatible — a new table; the previous release never reads or writes it.

-- Migration: user_activations
-- Created: 2026-11-13T11:11:25Z
--
-- A user's activation milestone (product event user_first_message), claimed
-- once and kept: the row is written in the same statement that checks the
-- qualifying message, and the user_id key makes the first writer the only
-- one across retries, replicas and processes. It references no message or
-- chat, so deleting a user's history cannot reset the milestone.
CREATE TABLE user_activations (
    user_id text PRIMARY KEY,
    -- The message that activated the user (no foreign key: the milestone
    -- outlives it).
    message_id text NOT NULL,
    chat_id text NOT NULL,
    activated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE user_activations IS 'forge:no-entity';
