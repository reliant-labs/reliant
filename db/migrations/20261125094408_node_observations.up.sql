-- forge:backward-compatible — adds two tables only this release reads and writes; the previous release never touches them, so after a rollback they are simply left unread.

-- Migration: node_observations
--
-- What a workflow node's `observe:` block recorded when the node finished
-- (dev-docs/design/workflow-observe.md, section 5): the attributes and events
-- its expressions produced, after redaction and the size caps. Metrics are not
-- stored here; they go to the metrics pipeline.
--
-- A sibling of step_executions, not a column on it: a node the workflow
-- completes itself (compute, join, router) writes no step row, and decisions
-- are kept on their own retention (worker config node_observation_retention).
--
-- Keyed by chat and run with no foreign key, like every run-scoped table
-- (internal/store/project_delete.go): account purge, project delete and the
-- ledger retention pass remove its rows by hand.
CREATE TABLE node_observations (
    id text PRIMARY KEY,
    chat_id text NOT NULL,
    workflow_id text NOT NULL,
    run_id text NOT NULL DEFAULT '',
    step_id text NOT NULL,
    node_path text NOT NULL DEFAULT '',
    loop_node_id text,
    loop_iteration bigint,
    attempt integer NOT NULL DEFAULT 1,
    source text NOT NULL DEFAULT 'workflow',
    attributes jsonb NOT NULL DEFAULT '{}',
    events jsonb NOT NULL DEFAULT '[]',
    errors jsonb NOT NULL DEFAULT '[]',
    truncated boolean NOT NULL DEFAULT false,
    created_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- One row per node attempt and source: an activity retry or a replayed
-- recording converges on the row already written (INSERT ... ON CONFLICT DO
-- NOTHING).
CREATE UNIQUE INDEX node_observations_node ON node_observations
    (workflow_id, step_id, node_path, coalesce(loop_node_id, ''),
     coalesce(loop_iteration, -1), attempt, source);

-- The per-chat read, and account purge and project delete.
CREATE INDEX node_observations_chat ON node_observations (chat_id, created_at);

-- The per-run row cap (2,000 rows a root run) counts by run.
CREATE INDEX node_observations_run ON node_observations (run_id) WHERE run_id <> '';

-- A node of a run still going may finish after DeleteUserProject purged its
-- chat. The chat purge's write admission (chat_write_admission,
-- 20261114221537 and 20261114225415) waits for a writer that got there first
-- and refuses one that comes later, so no observation outlives its chat.
CREATE TRIGGER chat_write_admission BEFORE INSERT ON node_observations
  FOR EACH ROW EXECUTE FUNCTION chat_write_admission();

COMMENT ON TABLE node_observations IS
    'What a workflow node''s observe: block recorded when it finished: attributes and events, redacted and size-capped. Removed by account purge, project delete and ledger retention.';
COMMENT ON COLUMN node_observations.run_id IS
    'The root run (forge.ai.run.id) the node ran in; empty when the activity carried no run telemetry.';
COMMENT ON COLUMN node_observations.source IS
    'Who recorded it: workflow (the observe: block) or model (a model-recorded event).';
COMMENT ON COLUMN node_observations.errors IS
    'Expressions that failed to evaluate, each its CEL message cut to 200 bytes. Never exported as telemetry.';

-- node_observation_retention_deferred_roots: root runs the observation prune
-- passed with part of the tree not yet prunable (running, paused, failed or
-- finished too recently). The prune's cursor (ledger_retention_cursors row
-- 'node_observations') moves on past them; every pass revisits these until
-- the whole tree has been pruned. step_retention_deferred_roots is the same
-- record for the step prune, which runs on its own retention period.
CREATE TABLE node_observation_retention_deferred_roots (
    root_id text PRIMARY KEY,
    deferred_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE node_observation_retention_deferred_roots IS
    'forge:no-entity';
