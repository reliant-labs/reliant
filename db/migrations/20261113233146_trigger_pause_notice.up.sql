-- forge:backward-compatible — adds two nullable columns with no default and no constraint; the previous release neither reads nor writes them, and its writes name their columns, so they stay NULL (a pause it makes has no paused_at, which this release reads as no pause notice to send).

-- Migration: trigger_pause_notice
-- Created: 2026-11-13T23:31:46Z
--
-- paused_at is when the system last paused the trigger (PauseTrigger): the
-- incident an automation paused for want of an org account is announced by
-- (control-plane docs/design/org-provider-accounts.md §11.4), to its owner
-- and the org's admins in the inbox, and by email to the admins.
-- pause_notice_for records that that email was delivered for exactly that
-- pause (sent, already sent, or nobody to send it to); a no_org_account pause
-- whose paused_at it does not equal (NULL included) still has its email to
-- deliver.

ALTER TABLE triggers ADD COLUMN paused_at timestamptz;
ALTER TABLE triggers ADD COLUMN pause_notice_for timestamptz;

COMMENT ON COLUMN triggers.paused_at IS 'When the system last paused the trigger (with paused_reason); meaningful only while enabled is false and paused_reason is set.';
COMMENT ON COLUMN triggers.pause_notice_for IS 'The paused_at whose email to the org''s owners and admins was delivered (no_org_account pauses only); a pause it does not equal still has its notice to deliver.';
