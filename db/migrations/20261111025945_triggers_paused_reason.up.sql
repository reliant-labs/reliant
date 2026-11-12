-- forge:backward-compatible — adds one nullable column with no default and no constraint; the previous release never reads or writes it, so its reads, inserts and enabled toggles keep working, and a value it leaves behind on a row it re-enables is ignored (paused_reason means something only while enabled is false).

-- Migration: triggers_paused_reason
--
-- Why a trigger is paused when the system paused it rather than a person:
-- 'creator_left_org' when a launch was refused because the trigger's creator
-- is no longer a live member of the trigger's org (control-plane
-- docs/design/orgs-and-members.md §5). NULL for a trigger that is enabled or
-- was paused by a person.
--
-- There is deliberately no CHECK tying it to enabled: the previous release
-- turns a trigger back on without knowing this column, and a constraint would
-- refuse that write after a rollback. Readers treat the column as meaningful
-- only while enabled is false, and this release clears it whenever it enables.
ALTER TABLE triggers ADD COLUMN paused_reason text;

COMMENT ON COLUMN triggers.paused_reason IS 'Why the system paused the trigger (creator_left_org); NULL when enabled or paused by a person. Meaningful only while enabled is false.';
