-- +goose Up

-- Activations of workflow-declared triggers (research/INTEGRATIONS_V1_BRIEF.md
-- §3a). A workflow's `triggers:` block declares WHEN it runs; a trigger row is
-- one user's activation of a declaration: who the runs execute as, in which
-- project, through which connection and daemon.
--
-- workflow_trigger names the declaration (WorkflowTrigger.name in the row's
-- `workflow`). NULL is an ad hoc trigger whose source is written inline, as
-- every trigger was before this.
--
-- For an activation, config and filter are a PROJECTION of the declaration,
-- refreshed by a periodic reconcile. They exist because routing reads them in
-- SQL (integration triggers by config->>'integration') and the schedule
-- syncer reads the cron. Whenever the trigger fires, the declaration is
-- re-read and is authoritative.
ALTER TABLE triggers ADD COLUMN workflow_trigger text;

-- The reconciler walks activations; ad hoc triggers are the majority and are
-- never touched by it.
CREATE INDEX idx_triggers_workflow_trigger ON triggers (user_id, workflow)
    WHERE workflow_trigger IS NOT NULL;
