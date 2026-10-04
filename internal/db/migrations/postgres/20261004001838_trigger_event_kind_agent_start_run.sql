-- +goose Up

-- A run started by an agent's start_run tool is a trigger event of its own
-- kind. It has no stored trigger behind it (like chat.start), but it records
-- who started it: the payload carries parent_chat_id, and that lineage is what
-- the fork-bomb guard walks to cap how deep agents can start agents.
--
-- The dedupe key is "<calling chat id>:<tool call id>", so a retried
-- ExecuteTools activity re-issuing the same call attaches to the run it
-- already started instead of launching a second one.
--
-- The kind CHECK is the only thing to widen; roll forward by replacing it.
ALTER TABLE trigger_events DROP CONSTRAINT trigger_events_kind_check;
ALTER TABLE trigger_events ADD CONSTRAINT trigger_events_kind_check
    CHECK (kind IN ('chat.start', 'schedule', 'agent.start_run'));
