-- forge:backward-compatible — adds defaulted columns and lets daemon_id be NULL for the new forge rows only; the previous release reads and writes workspace grants exactly as before.

-- Migration: connector_grant_kind
-- Created: 2026-11-13T15:14:19Z
--
-- A connector grant gains a KIND. Every grant until now is a "workspace"
-- grant: a third-party MCP client driving one daemon (daemon_id, path_root,
-- exec mode). The hosted forge MCP connector ("Reliant Forge", /forge/mcp)
-- needs an account-scoped grant instead: "forge: org X, projects Y (empty =
-- every project of X), these tools". It names no daemon and no path: a forge
-- row stores daemon_id NULL (the column references daemons(id)) and path_root
-- '', so daemon_id becomes nullable and both CHECKs say which kind needs what.
--
-- Existing rows default to 'workspace', so the previous release, which never
-- names the new columns, keeps reading and writing them unchanged. A forge
-- client binding is stored under a namespaced client id ("forge:<client>",
-- connectorgrant.BindingClientID), so connector_client_bindings and its
-- UNIQUE (user_id, client_id) are untouched.
--
-- Post-deploy verification (prod):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify connector_grant_kind" \
--     "SELECT kind, count(*) FROM connector_grants GROUP BY kind"
--
-- Rollback: none needed. The previous release ignores the new columns and
-- reads a NULL daemon_id (forge rows only) as empty; a forge
-- grant created meanwhile is invisible to the forge endpoint it no longer
-- serves, and the workspace endpoint of that release would offer it to the
-- one-live-connector fallback only if it were the user's only grant.
ALTER TABLE connector_grants
    ADD COLUMN kind text NOT NULL DEFAULT 'workspace',
    ADD COLUMN org_id text NOT NULL DEFAULT '',
    ADD COLUMN project_names jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE connector_grants
    ADD CONSTRAINT connector_grants_kind_valid CHECK (kind = ANY (ARRAY['workspace'::text, 'forge'::text])),
    ADD CONSTRAINT connector_grants_forge_org CHECK (kind <> 'forge' OR length(TRIM(BOTH FROM org_id)) > 0),
    ADD CONSTRAINT connector_grants_project_names_array CHECK (jsonb_typeof(project_names) = 'array');

ALTER TABLE connector_grants ALTER COLUMN daemon_id DROP NOT NULL;
ALTER TABLE connector_grants
    ADD CONSTRAINT connector_grants_daemon_by_kind CHECK ((kind = 'workspace') = (daemon_id IS NOT NULL));

ALTER TABLE connector_grants DROP CONSTRAINT connector_grants_path_root_not_empty;
ALTER TABLE connector_grants
    ADD CONSTRAINT connector_grants_path_root_not_empty CHECK (kind <> 'workspace' OR length(TRIM(BOTH FROM path_root)) > 0);
