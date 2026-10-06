-- +goose Up

-- Who or what an event came from, normalized at intake: trigger.sender.
-- {"kind", "id", "display_name", "verified"}, set from what the source
-- authenticated (a signed Slack or GitHub delivery, a Gmail message's DMARC
-- result, the webhook token), never from the payload. It is its own column
-- rather than a payload key because the payload is the sender's own data and
-- a key there is one the sender could write.
--
-- NULL for a start a person made themselves (a chat, a builder test) and for
-- every event recorded before this column existed; the runtime reads NULL as
-- an empty, unverified sender, so a filter that requires a verified sender
-- rejects those rather than guessing.
ALTER TABLE trigger_events ADD COLUMN IF NOT EXISTS sender jsonb;
