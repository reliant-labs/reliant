-- forge:backward-compatible — widens access_tokens_resource_kind to exactly accesstoken.AllResourceKinds (adds 'environment'); every row the previous release wrote still satisfies it.
-- Migration: access_tokens_resource_kind_environment
-- Created: 2026-10-22T09:45:15Z
--
-- The schema is the 29 migration(s) in this directory, applied in order (newest: 20261022081005_access_tokens_scopes_forge_obs_db.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Admit the 'environment' resource kind in access_tokens.resource_kind: forge
-- #1142 (3a6e69cd) added it to forge/pkg/accesstoken.AllResourceKinds, which
-- this forge pin brings in, so an environment-bound mint here would fail the
-- CHECK. Written by `forge db migration new --closed-set access_tokens_resource_kind`.

-- access_tokens_resource_kind on public.access_tokens mirrors forge:closed-set accesstoken.AllResourceKinds.
-- Replace it with a CHECK that lists exactly that set, in the set's order.
-- DROP and ADD are one ALTER TABLE: one lock, and no instant
-- without the constraint. postgres drops a constraint's comment with it, so the
-- declaration is re-issued after.
ALTER TABLE public.access_tokens
    DROP CONSTRAINT access_tokens_resource_kind,
    ADD CONSTRAINT access_tokens_resource_kind CHECK (((resource_kind IS NULL) OR (resource_kind = ANY (ARRAY['daemon'::text, 'port'::text, 'connector'::text, 'environment'::text]))));
COMMENT ON CONSTRAINT access_tokens_resource_kind ON public.access_tokens IS
    'forge:closed-set accesstoken.AllResourceKinds';
