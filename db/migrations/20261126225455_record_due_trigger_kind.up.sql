-- forge:backward-compatible — widens two CHECKs: every kind the previous release writes is still allowed, both new constraints are added NOT VALID so the swap holds its lock for the catalog change only, and the previous release reads a record_due trigger or event like any other kind it does not know (an automation, rendered generically).

-- Migration: record_due_trigger_kind
-- Created: 2026-11-26T22:54:55Z
--
-- record_due is a new trigger source (pipeline build plan task M4): a
-- workflow's declared trigger that fires when a record of a registered
-- table (pr_pipeline) falls due, found by the worker's due scan. Its stored
-- triggers row and its trigger_events rows carry the kind record_due, so
-- both kind CHECKs admit it. Each is dropped and re-added in one statement
-- (no instant without a check), NOT VALID, and validated by
-- 20261126225458_record_due_trigger_kind_validate under SHARE UPDATE
-- EXCLUSIVE. lock_timeout bounds the wait behind a long transaction. Inert:
-- nothing writes the kind until the pipeline ships behind `pipeline.enabled`.
--
-- Rollback: none needed. The previous release never writes record_due.
SET LOCAL lock_timeout = '5s';
ALTER TABLE triggers
    DROP CONSTRAINT triggers_kind_check,
    ADD CONSTRAINT triggers_kind_check CHECK (kind = ANY (ARRAY[
        'schedule'::text, 'webhook'::text, 'integration'::text, 'workflow_event'::text, 'record_due'::text
    ])) NOT VALID;
ALTER TABLE trigger_events
    DROP CONSTRAINT trigger_events_kind_check,
    ADD CONSTRAINT trigger_events_kind_check CHECK (kind = ANY (ARRAY[
        'chat.start'::text, 'schedule'::text, 'agent.start_run'::text, 'builder.test'::text,
        'webhook'::text, 'integration'::text, 'workflow_event'::text, 'manual.run'::text,
        'cli.run'::text, 'record_due'::text
    ])) NOT VALID;
