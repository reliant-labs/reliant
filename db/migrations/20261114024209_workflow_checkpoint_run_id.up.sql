-- forge:backward-compatible — one new nullable column; the previous release neither reads nor writes it, and its upsert leaves it as it is.

-- The run a position belongs to, for AI monitoring: the id its spans carry as
-- forge.ai.run.id (internal/telemetry/ai_attrs.go). A run keeps it across
-- continue-as-new in its own input; a restart from this checkpoint (a resume
-- of an interrupted run, which is a new Temporal execution) reads it here, so
-- the resumed run's spans group with the run it continues. NULL for a
-- checkpoint written before this column, or by a worker that predates it: the
-- resumed run then starts a run id of its own.
ALTER TABLE workflow_checkpoints ADD COLUMN run_id text;
COMMENT ON COLUMN workflow_checkpoints.run_id IS 'forge:readable an opaque run identifier (a Temporal run id); not a credential';
