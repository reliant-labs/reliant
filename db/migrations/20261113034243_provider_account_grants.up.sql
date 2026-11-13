-- forge:backward-compatible — adds a new table the previous release never reads or writes.

-- Migration: provider_account_grants
-- Created: 2026-11-13T01:50:21Z
--
-- Who may use an org account whose org_access is 'selected'
-- (org-provider-accounts.md §3.3): one row per (account, member).
--
-- A grant is bound to the membership it was made in. Control-plane owns
-- membership and reliant cannot share its transaction, so a removed member's
-- rows are not deleted with the membership: a grant counts only while the
-- member is live AND granted_at >= the membership's member_since (control-plane
-- ResolveOrgMembership). A person who leaves and rejoins starts with no grants.
-- Stale rows are inert and swept lazily.
--
-- org_id is the account's org, repeated so a member's grants in one org are one
-- index read; the store writes it from the account row. user_id is the
-- grantee, granted_by_user_id the admin who granted it (attribution only).
-- Nothing writes this table yet.
--
-- Post-deploy verification (prod): the table exists and is empty, since
-- nothing writes it in this release (0):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify provider_account_grants" "SELECT count(*) FROM provider_account_grants"
--
-- Rollback: none needed. The previous release never names the table; it stays
-- in place, unread. Its one index is built in this file because the table is
-- new and empty, so the build blocks nothing.
CREATE TABLE provider_account_grants (
    account_id         text NOT NULL REFERENCES provider_accounts(id) ON DELETE CASCADE,
    org_id             text NOT NULL,
    user_id            text NOT NULL,
    granted_by_user_id text NOT NULL,
    granted_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, user_id),
    CONSTRAINT provider_account_grants_ids_nonempty CHECK (org_id <> '' AND user_id <> '' AND granted_by_user_id <> ''),
    -- A grantee is a person, never an owner key.
    CONSTRAINT provider_account_grants_user_is_person CHECK (user_id NOT LIKE 'org:%')
);
CREATE INDEX provider_account_grants_org_user ON provider_account_grants (org_id, user_id);
