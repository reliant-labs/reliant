-- +goose Up
--
-- Adds is_forge to projects so we can query "how many forge projects" from
-- the DB and label analytics events with the project kind. A project is
-- "forge" when its repo root contains a forge.yaml — detection happens at
-- clone / project-create time, not lazily on read.
--
-- TODO(forge-backfill): backfill is_forge for existing projects on the next
-- daemon sync — read forge.yaml presence from each daemon's clone (same path
-- the skills catalog already inspects). Like remote_url, the source of truth
-- lives on the daemon's filesystem, not the DB, so the backfill is not run
-- from this migration.

ALTER TABLE projects ADD COLUMN is_forge BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE projects DROP COLUMN is_forge;
