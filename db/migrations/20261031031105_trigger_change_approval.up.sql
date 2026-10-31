-- forge:backward-compatible — adds two defaulted columns; the previous release neither reads nor writes them
-- Migration: trigger_change_approval
--
-- Each activation configuration change now records how it was approved: an
-- owner's card, the owner's automation-approvals setting (auto:<mode>), or a
-- pause (auto:pause); empty for a change the owner made in the app. The
-- capability change lists the capability items it added and removed, so an
-- auto-approved change is reviewable after the fact.
ALTER TABLE trigger_changes
    ADD COLUMN approval text NOT NULL DEFAULT '',
    ADD COLUMN capability_change jsonb;
