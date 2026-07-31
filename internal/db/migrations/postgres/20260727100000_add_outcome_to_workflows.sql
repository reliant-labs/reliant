-- +goose Up
-- The run's own VERDICT, separate from its lifecycle status.
--
-- status answers "did the machinery finish" and is Temporal-owned: the
-- reconciler rewrites it from the Temporal execution state, so it cannot carry
-- a workflow-semantic judgement — a graph that routes to its `failed` terminal
-- node still ends as a COMPLETED Temporal workflow and would be rewritten back
-- to completed. outcome answers "did the work pass", is written once by the
-- terminal node that declares it (Node.outcome in the workflow YAML), and is
-- never reconciled.
--
-- NULL means the workflow declared no outcome — NOT failure. Most workflows
-- declare nothing; only ones with an explicit pass/fail terminal do.
ALTER TABLE workflows ADD COLUMN IF NOT EXISTS outcome TEXT;

-- +goose Down
ALTER TABLE workflows DROP COLUMN IF EXISTS outcome;
