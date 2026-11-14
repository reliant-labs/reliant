-- forge:backward-compatible — adds one nullable column with no default and no constraint; the previous release neither reads nor writes it, and its inserts and updates name their columns, so they leave it NULL.

-- Migration: provider_accounts_reauth_notice
-- Created: 2026-11-13T21:58:08Z
--
-- A rejected org key's mark (needs_reauth_at) is the durable intent to email
-- the org's owners and admins about it; reauth_notice_for records that
-- Temporal accepted that email's workflow for exactly that mark. A mark whose
-- needs_reauth_at it does not equal (NULL included) has a notice still to
-- start: the next rejection, or the worker's sweep, starts it.

ALTER TABLE provider_accounts ADD COLUMN reauth_notice_for timestamptz;

COMMENT ON COLUMN provider_accounts.reauth_notice_for IS 'The needs_reauth_at whose email to the org''s owners and admins Temporal accepted (OrgKeyRejectedNoticeWorkflow); a mark it does not equal still has its notice to start. Org-owned accounts only.';
