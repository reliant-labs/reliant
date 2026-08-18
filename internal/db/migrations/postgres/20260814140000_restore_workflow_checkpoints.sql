-- +goose Up
-- Reverts the workflow position stack and restores the flat checkpoint it
-- replaced.
--
-- WHY THE STACK IS GOING AWAY. It reimplemented durable execution that Temporal
-- already provides. The one case a durable position genuinely had to cover is
-- the continueAsNew handoff at 40,000 events, and that path already carries its
-- own position: newContinueAsNewError passes Resume{NodeID, LoopIteration} in
-- the continuation's input, atomically with the handoff, which a separate table
-- write can only lag or fail behind. Everything else is covered by
-- reset-and-replay.
--
-- It was also broken in production. The table below was created with
-- workflow_name and workflow_hash NOT NULL, but the writer never plumbed either
-- through, so every position write failed at runtime with
--   null value in column "workflow_name" ... violates not-null constraint
--   (SQLSTATE 23502)
-- The build stayed green, so it only ever surfaced as runtime errors.
--
-- WHY A NEW MIGRATION RATHER THAN EDITING 20260814100000. That file was never
-- committed, but it HAD been applied to developer databases by the running app
-- (migrations run on startup), so those databases really do have
-- workflow_positions and really are missing workflow_checkpoints. Deleting the
-- file alone would leave them in that state with nothing to repair them. A new
-- forward migration converges both shapes: on a database that applied the stack
-- it drops the table and rebuilds the checkpoint; on a fresh database, where
-- 20260712000000 already created workflow_checkpoints and workflow_positions
-- never existed, both statements are no-ops. Editing history in place would
-- have fixed neither.
DROP TABLE IF EXISTS workflow_positions;

-- Recreated exactly as 20260712000000 declared it, with ONE deliberate change:
-- updated_at is TIMESTAMPTZ, not the original TIMESTAMP. A naive column drops
-- the offset, so which instant a row means depends on whether the writer
-- happened to pass local or UTC time — that silently corrupted ordering between
-- step_executions and workflows once already, and TestNoNaiveTimestampColumns
-- now fails the build on any naive column. Note the original table did not stay
-- naive either: 20260728000000 converted every timestamp column in the schema,
-- so timestamptz is what this table's readers were already seeing.
CREATE TABLE IF NOT EXISTS workflow_checkpoints (
    workflow_id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    loop_iteration BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_workflow_checkpoints_chat_id ON workflow_checkpoints(chat_id);

-- +goose Down
-- Down restores the position stack table so this migration is reversible in
-- isolation, but NOT its NOT NULL workflow_name/workflow_hash columns: those
-- are the defect this migration exists to remove, and recreating them would
-- rebuild a table no writer can insert into.
DROP TABLE IF EXISTS workflow_checkpoints;

CREATE TABLE IF NOT EXISTS workflow_positions (
    workflow_id TEXT NOT NULL,
    depth INTEGER NOT NULL,
    chat_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    in_loop BOOLEAN NOT NULL DEFAULT FALSE,
    iteration BIGINT NOT NULL DEFAULT 0,
    memo BOOLEAN NOT NULL DEFAULT FALSE,
    kind TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workflow_id, depth)
);

CREATE INDEX IF NOT EXISTS idx_workflow_positions_chat_id ON workflow_positions(chat_id);
