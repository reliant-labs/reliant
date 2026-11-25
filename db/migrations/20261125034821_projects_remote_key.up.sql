-- forge:backward-compatible — adds a function and a column the database computes (GENERATED ALWAYS ... STORED); the previous release never names either, so its reads, inserts and updates are unchanged, and the database fills the column for the rows it writes too.

-- Migration: projects_remote_key
-- Created: 2026-11-25T03:48:21Z
--
-- One repository is one project, on every machine that has it (a project
-- holds one checkout per machine in project_daemons). projects.remote_url is
-- the raw remote the creating machine reported, so the same repository read
-- `git@github.com:Org/Repo.git` on a laptop and
-- `https://github.com/org/repo` on a cloud machine, compared as strings, was
-- two repositories, and a second machine's folder became a second project.
--
-- remote_key is the remote reduced to `host/path`: no scheme, no user or
-- password, no port, the scp-like `host:path` read as `host/path`, no
-- trailing `/` or `.git`, all lowercase. Every form of one repo has one key.
-- NULL when there is no remote, or the remote names no host (a local path,
-- file://): a folder's path on disk is no identity across machines.
--
-- WHY GENERATED, NOT WRITTEN BY THE APP. The key has one definition, here, and
-- the database applies it to every row whoever writes it: the release before
-- this one (still writing remote_url during a rollout, or after a rollback),
-- the reconcile's remote_url backfill, an admin's UPDATE. A column the app
-- filled would need a backfill now and would be missing on every row an older
-- release writes. Lookups compare against git_remote_key(<the remote asked
-- about>) in SQL (internal/store GetProjectByRemoteKeyAndUserInScope), so no
-- second, Go, copy of the rule exists to drift from this one.
--
-- Changing the rule later is a new function and a new column (or, on Postgres
-- 17+, ALTER COLUMN ... SET EXPRESSION), never CREATE OR REPLACE of this one:
-- a stored generated column keeps the values the old body computed.
--
-- Not unique: a user may already have two projects of one repository (the
-- duplicates this stops making); a unique index would fail to build on them.
-- Merging those is the user's call (MergeProjects). The index is in the next
-- migration (CONCURRENTLY, alone in its file).
--
-- Adding a stored generated column rewrites the table under an ACCESS
-- EXCLUSIVE lock. projects is small (one row per user per repository), so the
-- rewrite takes milliseconds; lock_timeout bounds the wait behind a long
-- transaction: the migration fails after 5s and the next deploy retries it.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION git_remote_key(remote text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT CASE
        -- A host of two or more characters (a one-letter "host" is a Windows
        -- drive, C:\src), then at least one path segment; no backslash.
        WHEN k ~ '^[a-z0-9_][a-z0-9_.-]+(/[^/\\]+)+$' THEN k
    END
    FROM (
        SELECT lower(regexp_replace(regexp_replace(
            CASE
                -- scheme://[userinfo@]host[:port]/path
                WHEN r ~ '^[A-Za-z][A-Za-z0-9+.-]*://' THEN
                    regexp_replace(
                        regexp_replace(r, '^[A-Za-z][A-Za-z0-9+.-]*://', ''),
                        '^([^/@]*@)?([^/:]*)(:[0-9]*)?', '\2')
                -- scp-like [user@]host:path
                ELSE regexp_replace(r, '^([^/@:]*@)?([^/:]+):/*', '\2/')
            END,
            '/+$', ''), '(\.git)?/*$', '')) AS k
        FROM (SELECT btrim(remote, E' \t\r\n') AS r) AS raw
    ) AS keyed
$$;

COMMENT ON FUNCTION git_remote_key(text) IS
    'A git remote reduced to lowercase host/path (no scheme, userinfo, port, trailing / or .git; scp-like host:path read as host/path), or NULL when it names no host. projects.remote_key.';

ALTER TABLE projects
    ADD COLUMN remote_key text GENERATED ALWAYS AS (git_remote_key(remote_url)) STORED;

COMMENT ON COLUMN projects.remote_key IS
    'git_remote_key(remote_url): the repository, whatever form of its remote a machine reported. Not unique (see MergeProjects).';
