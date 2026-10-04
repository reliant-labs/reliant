-- +goose Up

-- A test run started from the workflow builder's Run tab is a launch event of
-- its own kind. It has no stored trigger behind it (like chat.start) and is
-- attended: a human pressed Run. The kind exists so the run can be told apart
-- from a real chat: it stays out of the sidebar (list_in_sidebar only admits
-- NULL, chat.start or adopted) and out of the default Runs list.
--
-- The kind CHECK is the only thing to widen; roll forward by replacing it.
ALTER TABLE trigger_events DROP CONSTRAINT trigger_events_kind_check;
ALTER TABLE trigger_events ADD CONSTRAINT trigger_events_kind_check
    CHECK (kind IN ('chat.start', 'schedule', 'agent.start_run', 'builder.test'));
