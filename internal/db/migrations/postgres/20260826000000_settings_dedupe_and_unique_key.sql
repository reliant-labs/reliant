-- +goose Up
-- Make a user-level setting unique on (user_id, key), and collapse the
-- duplicates that accumulated because it was not.
--
-- THE BUG. A setting is identified by (user_id, project_id, key), and the table
-- carries UNIQUE (user_id, project_id, key) to enforce that. But project_id is
-- NULL for every user-level setting — appearance.fontSize, appearance.theme,
-- and the rest — and in SQL NULL is never equal to NULL. A unique constraint
-- therefore never matches two such rows, and the constraint silently does not
-- apply to the rows the app writes most.
--
-- The consequence ran end to end. SettingsSyncService.syncToDatabase tries
-- CreateSetting first and falls back to UpdateSetting only on an ALREADY_EXISTS
-- conflict. That conflict never arrived, so every save INSERTed another row.
-- Observed in the dev database: six appearance.fontSize rows for one user,
-- values alternating md/lg. ListSettings returns them ORDER BY key, which is
-- not a total order over duplicates, and the client loads rows into
-- localStorage in sequence — so whichever duplicate happened to come last won,
-- and a months-old value could overwrite the one just chosen. To the user the
-- font size "did not save".
--
-- THE FIX, in the order it must happen:
--   1. Collapse existing duplicates, keeping the most recently updated row.
--   2. Add a partial unique index covering exactly the NULL-project_id case the
--      table constraint cannot reach.
-- CreateSetting then becomes a real upsert with this index as its conflict
-- target (see queries/settings.sql), so a save updates in place.
--
-- The original UNIQUE (user_id, project_id, key) stays: it still does the job
-- for project-scoped settings, where project_id is NOT NULL.

-- +goose StatementBegin
DELETE FROM settings a
USING settings b
WHERE a.project_id IS NULL
  AND b.project_id IS NULL
  AND a.user_id = b.user_id
  AND a.key = b.key
  AND (a.updated_at, a.id) < (b.updated_at, b.id);
-- +goose StatementEnd

CREATE UNIQUE INDEX IF NOT EXISTS settings_user_key_unique
    ON settings (user_id, key)
    WHERE project_id IS NULL;

-- +goose Down
DROP INDEX IF EXISTS settings_user_key_unique;
