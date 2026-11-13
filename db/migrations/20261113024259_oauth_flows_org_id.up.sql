-- forge:backward-compatible — adds one nullable column no previous release reads or writes; its flows keep working, and an org flow it might meet cannot be finished by it (the flow's session binding names the org).

-- Migration: oauth_flows_org_id
--
-- The organization an OAuth flow connects for: set when an org's owner or
-- admin connects an org-owned connection (control-plane
-- docs/design/org-sharing.md §7.1), NULL for a person's own connection, as
-- every flow before it. Completing the flow stores the connection as the
-- org's, sealed under the org's vault tenant.
--
-- A previous release that meets an org flow (a rollback inside a flow's
-- 10 minutes) ignores this column, and would otherwise finish it as a
-- personal connection. It cannot: the flow's session_id_hash binds the org
-- too, so its personal-binder check refuses it ("started by a different
-- account").
--
-- Adding a nullable column with no default is a catalog-only change; the
-- lock_timeout keeps it from queueing behind a long transaction.
SET LOCAL lock_timeout = '5s';
ALTER TABLE public.oauth_flows ADD COLUMN IF NOT EXISTS org_id text;
COMMENT ON COLUMN public.oauth_flows.org_id IS 'The organization an org-owned connection is being connected for; NULL for a personal connection';
