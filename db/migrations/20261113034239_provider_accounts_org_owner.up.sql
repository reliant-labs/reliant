-- forge:backward-compatible — nullable or defaulted columns the previous release never reads; its writes (owner_kind defaults to 'user', identity 'managed') satisfy every new CHECK, and the widened one_managed CHECK accepts all it accepted.

-- Migration: provider_accounts_org_owner
-- Created: 2026-11-13T01:49:21Z
--
-- Org-owned provider accounts and org-paid seats (control-plane
-- docs/design/org-provider-accounts.md §3.1, §3.2, §3.3, §3.8).
--
-- An org-owned account is a row whose user_id holds the OWNER KEY
-- 'org:<org_id>', with owner_kind = 'org' and org_id saying so. Both unique
-- keys and the priority sequence are (user_id, provider, ...), so an org gets
-- its own identity space and priority order with no constraint rewrite, and
-- every existing reader (WHERE user_id = <a person>) is blind to org rows: no
-- person's id is 'org:...'. Nothing writes an org row yet.
--
--   owner_kind          'user' (a person's) or 'org'.
--   org_id              the owning org, exactly when owner_kind = 'org'.
--   org_access          who in the org may use it: members | admins | selected
--                       (provider_account_grants), exactly for an org row.
--   created_by_user_id  the admin who added an org account. Attribution only,
--                       never authority; no foreign key, and an account purge
--                       of that person leaves the org's row alone.
--   attributed_org_id   on a PERSON's row: the org that pays for it (a Team
--   attribution_source  seat, or the managed key minted in that org), with how
--                       that was established: workspace | approved | managed.
--
-- Every CHECK is added NOT VALID, so the ACCESS EXCLUSIVE lock is held only for
-- the catalog change, never a scan; new and updated rows are checked from this
-- commit on. 20261113034240_provider_accounts_org_owner_validate validates the
-- existing rows in its own transaction under SHARE UPDATE EXCLUSIVE. Every
-- existing row has owner_kind 'user', a person's id and NULL in the new
-- columns, so validation cannot fail.
--
-- provider_accounts_one_managed is widened to admit one managed row per
-- (person, org): identity 'managed' (today's) or 'managed:<org_id>' (G0's per-org
-- managed key, not written yet). DROP and ADD are one ALTER TABLE: one lock, and
-- no instant without the constraint.
SET LOCAL lock_timeout = '5s';

ALTER TABLE provider_accounts
    ADD COLUMN owner_kind         text NOT NULL DEFAULT 'user',
    ADD COLUMN org_id             text,
    ADD COLUMN org_access         text,
    ADD COLUMN created_by_user_id text,
    ADD COLUMN attributed_org_id  text,
    ADD COLUMN attribution_source text;

ALTER TABLE provider_accounts ADD CONSTRAINT provider_accounts_owner_kind_check
    CHECK (owner_kind IN ('user', 'org')) NOT VALID;
-- org_id is set exactly on an org row, whose user_id is exactly its owner key;
-- a person's user_id never looks like one.
ALTER TABLE provider_accounts ADD CONSTRAINT provider_accounts_org_owner_key
    CHECK ((owner_kind = 'org') = (org_id IS NOT NULL)
       AND (org_id IS NULL OR (org_id <> '' AND user_id = 'org:' || org_id))) NOT VALID;
ALTER TABLE provider_accounts ADD CONSTRAINT provider_accounts_user_owner_key
    CHECK (owner_kind = 'org' OR user_id NOT LIKE 'org:%') NOT VALID;
-- An org owns API keys, never a sign-in: a subscription is a person's and is
-- never shared.
ALTER TABLE provider_accounts ADD CONSTRAINT provider_accounts_org_api_key_only
    CHECK (owner_kind = 'user' OR auth_kind = 'api_key') NOT VALID;
ALTER TABLE provider_accounts ADD CONSTRAINT provider_accounts_org_access_check
    CHECK ((org_access IS NULL) = (owner_kind = 'user')
       AND (org_access IS NULL OR org_access IN ('members', 'admins', 'selected'))) NOT VALID;
-- Attribution is a person's row only, and its source is set exactly with it.
ALTER TABLE provider_accounts ADD CONSTRAINT provider_accounts_attribution_check
    CHECK ((attributed_org_id IS NULL) = (attribution_source IS NULL)
       AND (attribution_source IS NULL OR attribution_source IN ('workspace', 'approved', 'managed'))
       AND (attributed_org_id IS NULL OR owner_kind = 'user')) NOT VALID;

ALTER TABLE provider_accounts
    DROP CONSTRAINT provider_accounts_one_managed,
    ADD CONSTRAINT provider_accounts_one_managed
        CHECK (auth_kind <> 'managed' OR identity_key = 'managed' OR identity_key LIKE 'managed:%') NOT VALID;
