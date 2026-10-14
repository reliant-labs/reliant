-- +goose Up

-- Whether Reliant caused an event, and which run did: trigger.origin, resolved
-- once at intake against the write ledger (integration_writes) and frozen on the
-- row, so the firing history can explain a skip ('origin_filter rejected: caused
-- by run X of this workflow') long after the ledger entry is pruned.
--
-- {"caused_by_reliant", "confidence", "run_id", "workflow", "trigger_name",
-- "same_workflow", "actor", "foreign", "lineage", "write_id"}.
--
-- NULL for an event no enricher looked at (every event recorded before this
-- column, and every non-integration kind); the runtime reads NULL as "not caused
-- by Reliant".
ALTER TABLE trigger_events ADD COLUMN IF NOT EXISTS origin jsonb;
