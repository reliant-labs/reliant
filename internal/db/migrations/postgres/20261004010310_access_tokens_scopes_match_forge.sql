-- +goose Up
-- +goose StatementBegin
-- The CHECK had drifted behind forge's closed scope set (accesstoken.AllScopes):
-- it lacked cluster:manage, daemon:resume, domain:read and domain:write. A mint
-- of any of them failed the constraint. It now lists exactly AllScopes;
-- TestAccessTokensScopesCheckMatchesForge pins the two together.
ALTER TABLE access_tokens DROP CONSTRAINT IF EXISTS access_tokens_scopes;
ALTER TABLE access_tokens ADD CONSTRAINT access_tokens_scopes CHECK (
    cardinality(scopes) > 0
    AND scopes <@ ARRAY[
        'deploy:read', 'deploy:write', 'cluster:manage',
        'token:read', 'token:write',
        'reliant:api', 'daemon:connect', 'llm:invoke',
        'proxy:port', 'mcp:connector', 'daemon:resume',
        'secret:read', 'secret:write',
        'domain:read', 'domain:write'
    ]::text[]
);
-- +goose StatementEnd
