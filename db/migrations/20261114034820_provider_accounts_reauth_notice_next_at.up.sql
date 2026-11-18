-- forge:backward-compatible — adds one nullable column with no default and no constraint; the previous release neither reads nor writes it, and its writes name their columns, so they leave it NULL (which this release reads as "due now").

-- Migration: provider_accounts_reauth_notice_next_at
-- Created: 2026-11-14T03:48:20Z
--
-- reauth_notice_next_at is when the worker's sweep may next take a rejected
-- org key whose notice is still to start (reauth_notice_for is not its
-- needs_reauth_at). The sweep claims the due marks in that order and moves it
-- forward as it claims each, so a mark whose start keeps failing comes round
-- again after the claim's lease and never holds the sweep's batch from a newer
-- mark (store.ClaimDueOrgKeyNotices). NULL, or a claim made before the current
-- mark, is due now.

ALTER TABLE provider_accounts ADD COLUMN reauth_notice_next_at timestamptz;

COMMENT ON COLUMN provider_accounts.reauth_notice_next_at IS 'When the worker''s sweep may next start the current rejection mark''s notice to the org''s owners and admins (NULL: now); moved forward as the sweep claims it. Org-owned accounts only.';
