-- forge:backward-compatible — widens a CHECK that every existing row already satisfies; the previous release mints only scopes the old list admitted, and the new list is a superset of it.

-- Admit identity:read and identity:manage in access_tokens.scopes.
--
-- forge #1060 (e93b712c) added them to forge's closed scope vocabulary
-- (forge/pkg/accesstoken.AllScopes) for control-plane's IdentityService:
-- identity:read reads an environment's managed-identity status and its end
-- users; identity:manage administers those users (invite, roles, lock, delete,
-- sessions, password-set codes, export) and implies identity:read. Both may
-- ride a daemon-bound token, so a mint of either here failed the CHECK.
--
-- The CHECK is replaced whole with exactly AllScopes, in forge's order.
-- TestAccessTokensScopesCheckMatchesForge (against a database) and
-- TestAccessTokensScopesMigrationMatchesForge (hermetic, runs under -short, so
-- on a forge-bump PR) pin the two together. DROP and ADD
-- are one ALTER TABLE: one lock acquisition, and no instant without the
-- constraint. The validating scan runs under that lock; access_tokens is small
-- (one row per issued credential), so it is not split into NOT VALID + VALIDATE.
ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scopes,
    ADD CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY[
            'deploy:read', 'deploy:write', 'cluster:manage',
            'token:read', 'token:write',
            'reliant:api', 'daemon:connect', 'llm:invoke',
            'proxy:port', 'mcp:connector', 'daemon:resume',
            'secret:read', 'secret:write',
            'domain:read', 'domain:write',
            'identity:read', 'identity:manage'
        ]::text[]
    );
