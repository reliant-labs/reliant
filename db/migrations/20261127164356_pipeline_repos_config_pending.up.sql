-- forge:backward-compatible — six new nullable columns on pipeline_repos; the previous release reads the table by named columns and never writes these, so its rows and queries are unchanged.

-- Migration: pipeline_repos_config_pending
-- Created: 2026-11-27T16:43:56Z
--
-- The pipeline.yaml read a base-branch push left to do. The PR record
-- projector runs inside webhook delivery and never waits on a daemon, so a
-- push that may have changed .reliant/pipeline.yaml records its commit here
-- (with the pusher, whom a gate stage change is attributed to when no merged
-- PR is), and a worker loop reads the file at that commit on a gate pool
-- daemon, records it through the same validation as before, and clears the
-- columns. A read that reaches no daemon is retried at
-- config_pending_retry_at, later after each attempt. Inert: nothing writes
-- the columns until the pipeline ships behind `pipeline.enabled`. No index:
-- the table holds one row per enrolled repository.
--
-- Rollback: none needed. The previous release never reads the new columns.
ALTER TABLE pipeline_repos
    -- The base-branch commit whose pipeline.yaml is still to be read; NULL
    -- when none is.
    ADD COLUMN config_pending_sha TEXT,
    -- When the push that left it was projected.
    ADD COLUMN config_pending_at TIMESTAMPTZ,
    -- The push's sender (provider user id and login), for a gate stage
    -- change the file carries.
    ADD COLUMN config_pending_sender_id TEXT,
    ADD COLUMN config_pending_sender_login TEXT,
    -- Reads of it started so far; NULL is none.
    ADD COLUMN config_pending_attempts INTEGER,
    -- When it is next due (a read in flight holds it ahead as a lease).
    ADD COLUMN config_pending_retry_at TIMESTAMPTZ;
