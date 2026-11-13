-- forge:backward-compatible — a new nullable column the previous release neither reads nor writes; every existing row is NULL, which reads as 'any', so every trigger runs as it does today.

-- Migration: trigger_unattended_funding
-- Created: 2026-11-13T16:53:39Z
--
-- Which AI accounts the runs a trigger fires may spend (control-plane
-- docs/design/org-provider-accounts.md §11.3, I4). A trigger's run has nobody
-- behind it to ask, so in an org each trigger is stamped when it is created,
-- from the org's shape at that moment:
--
--   org_only  only the org's own sources (its keys granted to the owner, its
--             Reliant credits), never the owner's personal accounts or seat;
--             the stamp of a trigger created in an org with two or more live
--             members
--   any       every source the owner's own runs may use; the stamp in a solo
--             org, and when the member count is unknown
--   NULL      the trigger predates the stamp (or has no org): reads as 'any',
--             which is how every trigger ran before it
--
-- Re-stamped the same way when it is adopted (for the adopter's org) and when
-- its project moves to another org. The owner can always tighten it to
-- org_only; loosening to any is refused in a team org (org_only_automation).
--
-- The CHECK is added NOT VALID: every existing row is NULL, so there is nothing
-- to validate, and the table is not scanned under its lock. New writes are
-- checked from now on.
--
-- Post-deploy verification (prod): the column exists and no row is stamped yet
-- (0 until a trigger is created or adopted under this release):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify triggers.unattended_funding" \
--     "SELECT count(*) FROM triggers WHERE unattended_funding IS NOT NULL"
--
-- Rollback: none needed. The previous release never names the column, and an
-- org_only trigger runs on its owner's own accounts again under it, as today
-- (the design's rollout note: the owner's own account, never another person's).
ALTER TABLE triggers ADD COLUMN unattended_funding text;
ALTER TABLE triggers ADD CONSTRAINT triggers_unattended_funding_check
    CHECK (unattended_funding IN ('org_only', 'any')) NOT VALID;
