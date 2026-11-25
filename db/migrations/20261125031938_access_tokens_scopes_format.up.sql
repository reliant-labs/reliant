-- forge:backward-compatible — widens access_tokens_scopes from the closed list accesstoken.AllScopes to a format check every listed scope satisfies; the previous release reads a format-valid entry it does not know (a new scope, a bundle:<name> ref) tolerantly (ResolveStored: skipped, logged, counted).
-- Migration: access_tokens_scopes_format
-- Created: 2026-11-25T03:19:38Z
--
-- The schema is the 223 migration(s) in this directory, applied in order (newest: 20261124223045_daemon_attachment_resource_stats.up.sql). To read it:
--   forge project shapes --kind table          every table, with the file:line declaring it
--   forge db introspect --dsn "$DATABASE_URL"  the schema a database actually has
--   forge db migration new <name> --context    a new file with the schema written into it
--
-- Write your migration SQL below:

-- access_tokens_scopes stops enumerating forge's catalog and checks the FORMAT
-- of every element instead (scope bundles, owner-approved 2026-11-25). The
-- vocabulary belongs to the write path, which already refuses an unknown scope
-- (accesstoken.NewSet / ParseScope), and the read path is tolerant
-- (accesstoken.ResolveStored), so a new forge scope no longer costs a widening
-- and a validate migration, and a `bundle:<name>` ref can be stored.
-- Predecessor: 20261123015915_access_tokens_mcp_scopes.up.sql, validated by
-- 20261123015916_access_tokens_mcp_scopes_validate.up.sql.
--
-- Every element must match ^[a-z]+(:[a-z]+)*$: lower-case words joined by
-- colons, so 'deploy:config:read' and 'bundle:admin' pass and '', 'Deploy:read',
-- 'deploy: read' and 'not:a-scope' do not. A CHECK cannot unnest, so:
--   1. the array round-trips through a ','-joined string. That holds only when
--      no element contains ',' and none is NULL (NULL joins as '', which
--      splits back as '' and not NULL), so the joined string's pieces ARE the
--      elements;
--   2. the joined string is one element pattern after another, comma-separated.
-- cardinality(scopes) > 0 is kept from the predecessor: a token with no scope
-- authenticates and can do nothing.
--
-- Every one of the 42 values the predecessor listed matches the format, so no
-- row it admitted is excluded and the separate validation is safe. No row or
-- column is changed. The closed-set declaration (accesstoken.AllScopes) is not
-- re-issued: postgres drops it with the old constraint, and the new one
-- enumerates nothing for `forge lint --closed-sets` to compare. The COMMENT
-- below, which carries no closed-set marker, says so on the constraint and
-- tells that lane the declaration was retired on purpose, not lost.
-- DROP and ADD are one ALTER TABLE: one lock, and no instant without the
-- constraint. The CHECK is added NOT VALID, so the ACCESS EXCLUSIVE lock is held
-- for the catalog change alone, not for a scan of the table; new and updated
-- rows are checked from this commit on. The next migration validates the
-- existing rows. lock_timeout bounds the wait for that lock, as in
-- 20261121164700_access_tokens_v190_scopes: behind a long transaction the
-- migration fails after 5s (and the next deploy retries it) instead of
-- queueing every token read behind it.
SET LOCAL lock_timeout = '5s';
ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scopes,
    ADD CONSTRAINT access_tokens_scopes CHECK (
        cardinality(scopes) > 0
        AND string_to_array(array_to_string(scopes, ',', ''), ',') = scopes
        AND array_to_string(scopes, ',') ~ '^[a-z]+(:[a-z]+)*(,[a-z]+(:[a-z]+)*)*$'
    ) NOT VALID;
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'format check: every element matches ^[a-z]+(:[a-z]+)*$ (a scope or a bundle:<name> ref); the vocabulary is the write path''s (accesstoken.NewSet), the read is tolerant (accesstoken.ResolveStored)';
