-- forge:backward-compatible — replaces a CHECK on a table nothing older reads or writes with a weaker one that every existing row satisfies

-- Migration: queue_allow_unisolated_landing
--
-- Owner decision, 2026-11-26 21:12Z: until dedicated sysbox gate machines
-- exist, the merge queue may land a batch gated on a plain runc container
-- (not isolated), for a repository that opts in. This is a known gap,
-- recorded on every such batch: the batch's verdict_json carries
-- "unisolated_accepted": true, written by the queue only when the repo's
-- pipeline.yaml sets gate.allow_unisolated. A runc batch without that mark
-- still never lands.

ALTER TABLE queue_batches DROP CONSTRAINT queue_batches_runc_never_lands;
ALTER TABLE queue_batches ADD CONSTRAINT queue_batches_runc_never_lands
    CHECK (NOT (isolation_backend = 'runc' AND state = 'landed'
                AND COALESCE(verdict_json ->> 'unisolated_accepted', 'false') <> 'true'));
