-- forge:backward-compatible — adds two nullable columns to pipeline_repos, which no released code writes or reads by position; every insert and update the previous release could make still succeeds.

-- Migration: pipeline_repos_config_error
-- Created: 2026-11-26T18:56:06Z
--
-- The projector (pipeline build plan task M3) reads .reliant/pipeline.yaml
-- from a repository's base_branch on each push to it. A file that cannot be
-- read or does not validate keeps the config in force (config_sha,
-- config_json) and is recorded here, so the enrolment's status can say why
-- the config did not move: config_error is the reason, config_error_sha the
-- base_branch commit it was read at. Both are cleared when a valid file is
-- recorded. Inert: nothing writes them until the pipeline ships behind
-- `pipeline.enabled`.
--
-- Rollback: none needed. The previous release never names these columns.
ALTER TABLE pipeline_repos
    ADD COLUMN config_error TEXT,
    ADD COLUMN config_error_sha TEXT;
