-- forge:backward-compatible — data-only: rewrites existing remote_url values in place (no DDL, no constraint), and the previous release reads a remote_url as an opaque string and authenticates through the git credential helper, never the URL.

-- Git remotes were persisted as the daemon read them from `git config
-- remote.origin.url`. Checkouts made by releases before 6efd6eaa embedded the
-- user's GitHub token in that URL (https://x-access-token:<token>@github.com/…),
-- so the token landed in projects.remote_url and repos.remote_url, unencrypted
-- and served back over the API. The stores now strip userinfo on write
-- (internal/gitremote.StripCredentials); this repairs the rows already there.
--
-- Same rule as StripCredentials: http(s), git:// and every other scheme lose
-- their whole userinfo; ssh://, git+ssh:// and ssh+git:// keep the user
-- (git@) and lose only a password; scp-style, file:// and paths are untouched.
-- Idempotent: a second run matches nothing.

CREATE OR REPLACE FUNCTION pg_temp.strip_remote_url_credentials(url text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
        WHEN url ~* '^(ssh|git\+ssh|ssh\+git)://[^/?#:@]*:[^/?#]*@'
            THEN regexp_replace(url, '^((?:ssh|git\+ssh|ssh\+git)://[^/?#:@]*):[^/?#]*@', '\1@', 'i')
        WHEN url ~* '^(ssh|git\+ssh|ssh\+git)://'
            THEN url
        WHEN url ~ '^[A-Za-z][A-Za-z0-9+.-]*://[^/?#]*@'
            THEN regexp_replace(url, '^([A-Za-z][A-Za-z0-9+.-]*://)[^/?#]*@', '\1')
        ELSE url
    END
$$;

-- repos.remote_url has no uniqueness constraint.
UPDATE repos
SET remote_url = pg_temp.strip_remote_url_credentials(remote_url)
WHERE remote_url IS NOT NULL
  AND remote_url IS DISTINCT FROM pg_temp.strip_remote_url_credentials(remote_url);

-- projects_user_remote_url_uniq is UNIQUE (user_id, remote_url): the clean form
-- of a leaked URL can equal another of the same user's projects (the same repo
-- added once with a token in its remote and once without). The first project
-- by id keeps the cleaned URL; any other, and any whose clean form is already
-- taken, gets NULL. A project with no remote_url gets it back from the daemon's
-- reconcile, which already respects the index.
WITH dirty AS (
    SELECT id, user_id,
           pg_temp.strip_remote_url_credentials(remote_url) AS clean,
           row_number() OVER (
               PARTITION BY user_id, pg_temp.strip_remote_url_credentials(remote_url)
               ORDER BY id
           ) AS rn
    FROM projects
    WHERE remote_url IS NOT NULL
      AND remote_url IS DISTINCT FROM pg_temp.strip_remote_url_credentials(remote_url)
)
UPDATE projects p
SET remote_url = CASE
        WHEN d.rn = 1 AND NOT EXISTS (
            SELECT 1 FROM projects o
            WHERE o.user_id = p.user_id AND o.remote_url = d.clean AND o.id <> p.id
        ) THEN d.clean
        ELSE NULL
    END
FROM dirty d
WHERE p.id = d.id;
