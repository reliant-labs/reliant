-- forge:backward-compatible — a new table; the previous release never reads it and keeps using chats.queued_for_machine_at alone, which still marks a queued message.

-- What a chat's queued message is waiting for, and how many times delivering
-- it has been tried. chats.queued_for_machine_at says THAT a message is
-- queued; this row says for which machine, and bounds the retries.
--
-- Written when a run's preflight gives up on a machine and queues the message:
-- the selector it waited on (the workflow's pinned machine, else the chat's).
-- Delivery waits for THAT machine instead of re-deriving "the chat's machine",
-- which the run did not wait on.
--
-- attempts counts the runs queued delivery started for the message, spaced by
-- next_attempt_at (exponential backoff). It resets when a run gets past
-- preflight (the machine was there) and when the user sends a message. Past
-- the limit delivery stops retrying on its own: it clears the chat's queued
-- marker (the chat reads as a failed run, not as waiting), parks this row
-- (gave_up_at, held for ten minutes in next_attempt_at) and tells the chat's
-- owner. Once the hold has passed, the target machine having connected afresh
-- since (daemon_attachment.attached_at after gave_up_at) buys exactly one more
-- attempt; the owner's own message forgets the row.
CREATE TABLE chat_queued_deliveries (
    chat_id text PRIMARY KEY REFERENCES chats (id) ON DELETE CASCADE,
    -- The selector the run waited on; all empty means the chat's own machine.
    daemon_id text NOT NULL DEFAULT '',
    daemon_name text NOT NULL DEFAULT '',
    daemon_type text NOT NULL DEFAULT '',
    -- JSON object of the selector's label criteria; empty when it has none.
    daemon_labels text NOT NULL DEFAULT '',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    -- When this stretch of failed deliveries began.
    queued_since timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_failure text NOT NULL DEFAULT '',
    gave_up_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE chat_queued_deliveries IS 'forge:no-entity';
