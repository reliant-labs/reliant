-- +goose Up

-- When a queued message cut a waiting tool call short (ToolDefinition.Preemptable).
--
-- A message that arrives for a thread while it waits on shell_wait or
-- spawn_status(wait) ends the wait early, so the agent reads it now rather than
-- when the wait runs out. Recording that here is what bounds it to ONCE per
-- message: a message the drain cannot deliver stays queued, and if its mere
-- presence preempted every wait, the agent would loop on one-second waits and
-- LLM turns for as long as it stayed there. Once marked, it is left to the
-- drain, exactly as every message was before preemption existed.
--
-- Every statement is re-runnable (see 20261005170614).
ALTER TABLE agent_messages ADD COLUMN IF NOT EXISTS preempted_at timestamptz;
