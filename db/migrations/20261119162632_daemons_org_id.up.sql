-- forge:backward-compatible — adds one nullable column; the previous release never names daemons.org_id, its upserts leave it NULL (or as this release wrote it), and every column it reads keeps its name and type.

-- Migration: daemons_org_id
-- Created: 2026-11-19T16:26:32Z
--
-- The organization a machine belongs to. control-plane owns it
-- (controlplane.daemons.org_id); reliant mirrors it here when the machine
-- connects with a daemon-bound credential whose introspection names it
-- (AccessTokenInternalService.Introspect's daemon_org_id). Reliant keeps a
-- machine's project checkouts inside that org: the connect reconcile, attach
-- and the create-time checks match only that org's projects
-- (internal/handlers/tools_daemon/tools_daemon_reconcile.go).
--
-- NULL is a machine whose org reliant has not been told: every row written
-- before this release, a machine that connected only with an unbound login
-- token, and any machine of a control plane that predates daemon_org_id. It
-- keeps the behaviour of before: the machine matches its owner's projects of
-- every org. There is no foreign key: the org is control-plane's.
--
-- The CHECK keeps '' out, as on projects.org_id: an org is named or NULL. The
-- column is new and NULL in every row, so the check scans nothing it could
-- refuse; lock_timeout bounds the wait for the ACCESS EXCLUSIVE lock behind a
-- long transaction (the migration fails after 5s and the next deploy retries).
SET LOCAL lock_timeout = '5s';

ALTER TABLE daemons
    ADD COLUMN org_id text,
    ADD CONSTRAINT daemons_org_id_not_blank CHECK (org_id <> '');
COMMENT ON COLUMN daemons.org_id IS
    'The organization (control-plane organizations.id) the machine belongs to, mirrored from control-plane when it connects with a daemon-bound credential. NULL: not known yet; the machine then matches its owner''s projects of every org.';
