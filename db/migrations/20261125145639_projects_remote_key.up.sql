-- forge:backward-compatible — adds a function and a column the database computes (GENERATED ALWAYS ... STORED); the previous release never names either, so its reads, inserts and updates are unchanged, and the database fills the column for the rows it writes too.

-- Migration: projects_remote_key
-- Created: 2026-11-25T14:56:39Z
--
-- One repository is one project, on every machine that has it (a project
-- holds one checkout per machine in project_daemons). projects.remote_url is
-- the raw remote the creating machine reported, so the same repository read
-- `git@github.com:Org/Repo.git` on a laptop and
-- `https://github.com/org/repo` on a cloud machine, compared as strings, was
-- two repositories, and a second machine's folder became a second project.
--
-- remote_key is the repository a remote names, or NULL. A key equates two
-- remotes only where the equivalence is established; anything else keeps the
-- remote's own identity, so a false "same repository" (which would attach a
-- folder to an unrelated project) is never traded for a missed duplicate
-- (which the user can merge):
--
--   * github.com, gitlab.com, bitbucket.org, on their default ports: these
--     hosts serve one repository at the same owner/path over https, http,
--     ssh, scp-like ssh and git://, ignore a trailing .git, and route owner
--     and repository names case-insensitively. Key: `github.com/acme/props`
--     (lowercase, no scheme, user, port or .git; at least owner/repo).
--   * every other host: the transport, the ssh user, the host (lowercase:
--     DNS names are) and any non-default port are kept, and the path is kept
--     as written (case and .git included), with only trailing slashes
--     dropped. The scp-like `[user@]host:path` is git's
--     `ssh://[user@]host/~/path` (home-relative) or, for `host:/abs`,
--     `ssh://[user@]host/abs`, and keyed in that form. Default ports (https
--     443, http 80, ssh 22, git 9418) are the same server. Key:
--     `https://git.example.com:8443/Team/Props.git`.
--   * NULL: no remote, or one that names no server: a local path (absolute,
--     relative like `repos/props`, or a Windows drive), file://, any other
--     scheme, a host with no repository path, or anything unparseable. git's
--     own rule decides scp-like vs local: a colon before the first slash.
--     A path on disk is no identity across machines.
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
-- EXCLUSIVE lock. Do not assume a production table size or rewrite duration:
-- lock_timeout bounds acquisition, while statement_timeout cancels a rewrite
-- exceeding 5s (including lock acquisition). Cancellation rolls this entire
-- migration back, releasing its lock; the next deploy retries it. If the
-- bound is insufficient, measure and plan a follow-up rather than extending
-- the production blocking window implicitly. LOCAL keeps both bounds inside
-- this migration's transaction, not the subsequent concurrent index build.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5s';

CREATE FUNCTION git_remote_key(remote text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE
AS $$
DECLARE
    r      text := btrim(remote, E' \t\r\n');
    m      text[];
    family text;
    usr    text;
    host   text;
    port   text;
    path   text;
    dflt   text;
    homerel boolean := false;
BEGIN
    IF r ~ '^[A-Za-z][A-Za-z0-9+.-]*://' THEN
        -- scheme://[userinfo@]host[:port][/path]; no query or fragment.
        m := regexp_match(r,
            '^([A-Za-z][A-Za-z0-9+.-]*)://(?:([^/]*)@)?(\[[0-9A-Fa-f:.]+\]|[^/:?#@\[\]]+)(?::([0-9]*))?(/[^?#]*)?$');
        IF m IS NULL THEN
            RETURN NULL;
        END IF;
        family := CASE lower(m[1])
            WHEN 'https' THEN 'https'
            WHEN 'http' THEN 'http'
            WHEN 'ssh' THEN 'ssh'
            WHEN 'git+ssh' THEN 'ssh'
            WHEN 'ssh+git' THEN 'ssh'
            WHEN 'git' THEN 'git'
        END;
        IF family IS NULL THEN
            RETURN NULL; -- file:// and every other scheme name no server
        END IF;
        -- Only an ssh user names something (the account, whose home a ~ path
        -- is under); an http(s) userinfo is a credential.
        IF family = 'ssh' THEN
            usr := nullif(split_part(m[2], ':', 1), '');
        END IF;
        host := lower(m[3]);
        port := nullif(m[4], '');
        path := coalesce(m[5], '');
    ELSE
        -- scp-like [user@]host:path, by git's rule: a colon before the first
        -- slash. A one-letter "host" is a Windows drive (C:\src, C:/src).
        m := regexp_match(r, '^(?:([^/@:]*)@)?(\[[0-9A-Fa-f:.]+\]|[^/:\\@\[\]]{2,}):(.*)$');
        IF m IS NULL THEN
            RETURN NULL; -- a local path
        END IF;
        family := 'ssh';
        usr := nullif(m[1], '');
        host := lower(m[2]);
        path := m[3];
        IF left(path, 1) <> '/' THEN
            -- Home-relative: on a hosting provider just the repository's
            -- path, elsewhere ssh://host/~/path (below).
            homerel := true;
            path := '/' || path;
        END IF;
    END IF;

    -- The default port is the same server. (Read into a variable: an IF
    -- condition ends at its first THEN, a CASE's included.)
    dflt := CASE family WHEN 'https' THEN '443' WHEN 'http' THEN '80' WHEN 'ssh' THEN '22' WHEN 'git' THEN '9418' END;
    IF port = dflt THEN
        port := NULL;
    END IF;
    path := regexp_replace(path, '/+$', '');
    IF path !~ '^(/[^/\\]+)+$' THEN
        RETURN NULL; -- no repository path, an empty segment, or a backslash
    END IF;

    IF host IN ('github.com', 'gitlab.com', 'bitbucket.org') AND port IS NULL THEN
        path := lower(regexp_replace(ltrim(path, '/'), '\.git$', ''));
        IF path !~ '^[^/]+(/[^/]+)+$' OR path ~ '(^|/)~' THEN
            RETURN NULL; -- not owner/repo
        END IF;
        RETURN host || '/' || path;
    END IF;

    IF homerel THEN
        path := CASE WHEN left(path, 2) = '/~' THEN path ELSE '/~' || path END;
    END IF;
    RETURN family || '://' || coalesce(usr || '@', '') || host || coalesce(':' || port, '') || path;
END
$$;

COMMENT ON FUNCTION git_remote_key(text) IS
    'The repository a git remote names: lowercase owner/repo on github.com, gitlab.com and bitbucket.org; transport://[ssh user@]host[:non-default port]/path as written elsewhere; NULL for a local path or anything naming no server. projects.remote_key; see 20261125034821_projects_remote_key.';

ALTER TABLE projects
    ADD COLUMN remote_key text GENERATED ALWAYS AS (git_remote_key(remote_url)) STORED;

COMMENT ON COLUMN projects.remote_key IS
    'git_remote_key(remote_url): the repository, in any form of its remote known to name it. Not unique (see MergeProjects).';
