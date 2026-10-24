-- forge:backward-compatible — widens the scope vocabulary with obs:write; every row the previous release wrote remains valid.
-- This changes only the allowed vocabulary, never an existing token's grants.
ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scopes,
    ADD CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY[
            'deploy:read',
            'deploy:write',
            'cluster:manage',
            'token:read',
            'token:write',
            'reliant:api',
            'daemon:connect',
            'llm:invoke',
            'proxy:port',
            'mcp:connector',
            'daemon:resume',
            'secret:read',
            'secret:write',
            'domain:read',
            'domain:write',
            'identity:read',
            'identity:manage',
            'obs:read',
            'obs:triage',
            'obs:write',
            'db:query'
        ]::text[]
    );
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'forge:closed-set accesstoken.AllScopes';

