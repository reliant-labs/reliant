-- +goose Up
-- Provider backoff marker: the durable evidence that a thread is parked in an
-- LLM provider's rate-limit ladder rather than doing work.
--
-- The retry ladder runs entirely inside ONE Temporal activity attempt, so
-- without a marker written before the sleep the run emits nothing at all while
-- it waits — no message, no step execution, no state change. Measured on
-- forge-one-shot run b7aa4056: eight of ten fan-out units spent ~113s of their
-- ~129s life in 429 backoff, and every supervision surface reported them as
-- working.
--
-- One row per THREAD (threads are unique across chats, and thread is the unit
-- `workflow ps` reports). waiting_since is NULL when the thread is not currently
-- parked; the cumulative columns survive the run so `workflow analyze` can
-- answer where the time went after the fact.
CREATE TABLE IF NOT EXISTS provider_backoff (
    thread_id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    waiting_since TIMESTAMP,
    resume_at TIMESTAMP,
    attempt BIGINT NOT NULL DEFAULT 0,
    max_attempts BIGINT NOT NULL DEFAULT 0,
    status_code BIGINT NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    retries BIGINT NOT NULL DEFAULT 0,
    waited_ms BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_provider_backoff_chat_id ON provider_backoff(chat_id);

-- +goose Down
DROP TABLE IF EXISTS provider_backoff;
