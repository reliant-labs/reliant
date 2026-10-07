-- +goose Up
--
-- EXPAND: a step_executions row records what the step was GIVEN and how it
-- ended, not only what it produced, so a run can be debugged from its record
-- (the builder's Run tab; research/WORKFLOW_EDITOR_UX_REVIEW.md §4 E).
--
--   input_json     the node's resolved args — what its {{ }} expressions
--                  evaluated to — bounded by the writer (long strings are cut
--                  and marked), NULL for activities that are not graph nodes.
--   error_message  why this attempt failed, NULL when it did not.
--   attempt        Temporal's attempt number (1-based). A retried step writes
--                  one row per attempt; this is what tells two attempts apart
--                  from two runs of the same node.
--   node_path      the node's dotted graph position ("agent.agent_loop.call_llm"),
--                  so everything a top-level step ran — an Agent step's own
--                  turns — is found by prefix without guessing from step ids.
--
-- All nullable and unread by the previous release, which keeps writing rows
-- without them. No backfill: older rows simply have no record of their inputs.
ALTER TABLE step_executions
    ADD COLUMN IF NOT EXISTS input_json text,
    ADD COLUMN IF NOT EXISTS error_message text,
    ADD COLUMN IF NOT EXISTS attempt integer,
    ADD COLUMN IF NOT EXISTS node_path text;
