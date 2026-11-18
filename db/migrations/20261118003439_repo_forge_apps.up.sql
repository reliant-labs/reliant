-- forge:backward-compatible — adds a new table and a nullable column the previous release never reads or writes.

-- Migration: repo_forge_apps
-- Created: 2026-11-18T00:34:39Z
--
-- Every forge app in each of a project's repos (control-plane
-- docs/design/unified-shell-and-projects.md §8.1, D3; U10): the daemon's
-- repo.discover walks each repo for forge.yaml files (depth <= 4, skipping
-- node_modules, vendor, .git, gen and the like) and reports each one's
-- `name` and the directory it sits in. One row per (repo, subpath); subpath is
-- the forge.yaml's directory relative to the repo root, '' for the root
-- itself. forge_name is the app's name, resolved in the project's org.
--
-- repos.forge_apps_scanned_at is when a daemon last reported a COMPLETE scan
-- of the repo. NULL means unknown: no daemon has scanned it yet (every repo
-- this migration finds, and any repo whose daemon predates the scan). A
-- repo with a stamp and no rows has no forge app. projects.is_forge and
-- projects.forge_project_name are untouched and keep their meaning (the
-- project root's forge.yaml).
--
-- Post-deploy verification (prod): the table exists; it fills as daemons
-- connect and projects are created or cloned:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify repo_forge_apps" "SELECT count(*) FROM repo_forge_apps"
--
-- Rollback: none needed. The previous release never names the table or the
-- column; both stay in place, unread. The column is nullable with no
-- default, so adding it rewrites nothing; the table's index is built here
-- because the table is new and empty.
CREATE TABLE repo_forge_apps (
    repo_id    text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    subpath    text NOT NULL,
    forge_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, subpath),
    CONSTRAINT repo_forge_apps_name_nonempty CHECK (forge_name <> '')
);
-- The app page's "projects that contain this app" (U13) looks a name up.
CREATE INDEX repo_forge_apps_forge_name ON repo_forge_apps (forge_name);

ALTER TABLE repos ADD COLUMN forge_apps_scanned_at timestamptz;
