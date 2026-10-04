-- +goose Up

-- A user can have many daemons, so a trigger has to say which one its runs
-- execute on. Every tool call of a launched run goes to this daemon, and the
-- delegated daemon:resume credential is bound to the same (user, daemon) pair
-- rather than to everything the user owns.
--
-- No foreign key: reliant's daemons table is a cache of the control plane's
-- and rows can lag it, so the handler validates existence at write time and
-- the fire path re-checks it before launching.
ALTER TABLE triggers ADD COLUMN daemon_id TEXT;

-- Triggers are brand new and unreleased, so backfill to the owner's most
-- recently updated daemon rather than inventing a nullable "any daemon"
-- state that every reader would then have to handle.
UPDATE triggers t SET daemon_id = (
    SELECT d.id FROM daemons d
    WHERE d.user_id = t.user_id
    ORDER BY d.updated_at DESC, d.id
    LIMIT 1
);

-- A row whose owner has no daemon cannot be given a valid one. None exist in
-- prod: triggers is a pre-release table, so this only clears dev databases.
-- Its events keep their history (trigger_id is ON DELETE SET NULL).
DELETE FROM triggers WHERE daemon_id IS NULL;

ALTER TABLE triggers ALTER COLUMN daemon_id SET NOT NULL;
