-- +goose Up

-- A project_daemons row has meant "this project IS cloned on this daemon"
-- since the table existed: the frontend wrote it only after a clone had been
-- reported complete. CreateProjectFromRepo moves the clone server-side and
-- returns as soon as the command is QUEUED, so the row now has to be able to
-- say "a clone is on its way" as well.
--
-- Without this the RPC has two bad options: write the row up front and lie
-- about the checkout existing, or withhold it and give the user no project to
-- look at while a cold machine boots. The state column is what lets it do
-- neither.
--
-- Existing rows are backfilled to 'installed' by the DEFAULT: every one of
-- them was written by the old flow, which only ever wrote after a completed
-- clone. That is a true statement about them, not a convenient one.
ALTER TABLE project_daemons
    ADD COLUMN IF NOT EXISTS install_state TEXT NOT NULL DEFAULT 'installed',
    -- Why the clone failed, when it did. Surfaced verbatim in the UI, so the
    -- daemon-side message must stay user-safe.
    ADD COLUMN IF NOT EXISTS install_error TEXT NOT NULL DEFAULT '',
    -- Correlates the queued daemon command with this row, so the outcome
    -- notification can find what to update. Empty for rows that were never
    -- queued (a local clone, or anything the old flow wrote).
    ADD COLUMN IF NOT EXISTS install_request_id TEXT NOT NULL DEFAULT '';

-- Constrain the state to the three it can be in. A typo in application code
-- should fail the write, not silently create a fourth state that every
-- reader then has to guess about.
ALTER TABLE project_daemons
    DROP CONSTRAINT IF EXISTS project_daemons_install_state_check;
ALTER TABLE project_daemons
    ADD CONSTRAINT project_daemons_install_state_check
    CHECK (install_state IN ('installing', 'installed', 'failed'));

-- The outcome notification arrives keyed by request id, with no project id on
-- it, so that lookup needs an index. Partial: only queued rows are ever
-- looked up this way, and they are a small minority.
CREATE INDEX IF NOT EXISTS idx_project_daemons_install_request
    ON project_daemons (install_request_id)
    WHERE install_request_id <> '';
