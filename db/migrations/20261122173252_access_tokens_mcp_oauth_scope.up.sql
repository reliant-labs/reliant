-- forge:backward-compatible — widens the allowed token scopes; all scopes written by the previous release remain valid.

-- Migration: access_tokens_mcp_oauth_scope
-- Created: 2026-11-22T17:32:52Z
--
-- The schema is the 208 migration(s) in this directory, applied in order (newest: 20261121210232_projects_drop_user_remote_url_uniq.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Match the forge pin's accesstoken.AllScopes, including MCP OAuth.
-- Keep the exclusive lock to a catalog change; validate existing rows separately.
SET LOCAL lock_timeout = '5s';
ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scopes,
    ADD CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY[
            'deploy:read',
            'deploy:write',
            'deploy:config:read',
            'billing:read',
            'cluster:manage',
            'token:read',
            'token:write',
            'reliant:api',
            'daemon:connect',
            'llm:invoke',
            'proxy:port',
            'mcp:connector',
            'mcp:oauth',
            'daemon:resume',
            'daemon:manage',
            'secret:read',
            'secret:write',
            'domain:read',
            'domain:write',
            'identity:read',
            'identity:manage',
            'obs:read',
            'obs:triage',
            'obs:write',
            'db:query',
            'storage:read',
            'runs:start',
            'runs:read',
            'runs:control',
            'triggers:fire',
            'triggers:manage',
            'inbox:read',
            'inbox:manage',
            'approvals:decide',
            'questions:answer',
            'sourcemaps:write',
            'changes:write',
            'changes:launch',
            'vuln:read',
            'tests:read',
            'tests:write'
        ]::text[]
    ) NOT VALID;
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'forge:closed-set accesstoken.AllScopes';

