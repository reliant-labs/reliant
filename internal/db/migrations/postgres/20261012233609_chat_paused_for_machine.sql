-- +goose Up

-- Why a chat is paused, when the reason is "its machine dropped". The
-- daemon-offline circuit breaker pauses a run after it has spent two minutes
-- unable to reach its machine; the run stays parked until something resumes
-- it. This column marks THAT pause, so that when the chat's machine connects
-- the server resumes it (internal/queueddelivery) without resuming a chat the
-- user paused on purpose. Set when the breaker pauses; cleared by every
-- resume, by the user's pause, and by a send. Deliberately not in
-- chats_with_activity: it drives no UI state, only that one query.
--
-- Re-runnable (see 20261005170614).
ALTER TABLE chats ADD COLUMN IF NOT EXISTS paused_for_machine_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_chats_paused_for_machine
    ON chats (paused_for_machine_at)
    WHERE paused_for_machine_at IS NOT NULL;

