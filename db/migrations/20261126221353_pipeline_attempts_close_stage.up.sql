-- forge:backward-compatible — widens pipeline_attempts_stage by one value; every row the previous release could write still satisfies it, and the previous release never reads stage close.

-- Migration: pipeline_attempts_close_stage
-- Created: 2026-11-26T22:13:53Z
--
-- A close is recorded as an attempt (stage close) of the PR it closed, with
-- the evidence's signature (pipeline build plan M13; redesign §6.4). That is
-- what lets the pipeline keep its word to a person who reopens a PR it
-- closed: pr.next and the daily PR sweep never close it again for the same
-- evidence. Inert: nothing writes the table until the pipeline ships behind
-- `pipeline.enabled`.
--
-- The CHECK is dropped and re-added in one ALTER TABLE: one lock and no
-- instant without it. It is added valid: a widening never invalidates a row.
-- postgres drops a constraint's comment with it, so the closed-set
-- declaration is re-issued. lock_timeout bounds the wait.
--
-- Rollback: none needed. The previous release never writes or reads stage
-- close.
SET LOCAL lock_timeout = '5s';

ALTER TABLE pipeline_attempts
    DROP CONSTRAINT pipeline_attempts_stage,
    ADD CONSTRAINT pipeline_attempts_stage CHECK (stage IN ('review', 'fix', 'gate', 'respond', 'close'));

COMMENT ON CONSTRAINT pipeline_attempts_stage ON pipeline_attempts IS
    'forge:closed-set github.com/reliant-labs/reliant/internal/db/core.AllPipelineAttemptStages';
