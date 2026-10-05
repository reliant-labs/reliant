-- +goose Up

-- video_generation_jobs: one row per generate_video tool call, written BEFORE
-- the first poll.
--
-- A render takes 30s to several minutes and the provider bills it on
-- completion. If the worker dies mid-render the activity is re-dispatched with
-- the same tool call id; without a durable record of the provider's job id the
-- retry would Submit again and pay for a second clip. With it, the retry finds
-- the row and Polls the job that is already running.
--
-- It also backs conversational edit: the agent's edit handle is the previous
-- clip's attachment id, and this row maps it back to the provider interaction
-- that produced it (and proves the caller owns it).
--
-- No chat FK: the row must outlive a deleted chat only as long as the clip's
-- attachment does, and the attachment has no chat FK either.
CREATE TABLE IF NOT EXISTS video_generation_jobs (
    tool_call_id   text PRIMARY KEY,
    user_id        text NOT NULL,
    chat_id        text NOT NULL DEFAULT '',
    driver         text NOT NULL,
    model_id       text NOT NULL,
    api_model      text NOT NULL,
    -- Provider handle: Veo operation name or Omni interaction id.
    provider_job   text NOT NULL,
    -- submitted | completed | failed | cancelled
    state          text NOT NULL,
    attachment_id  text,
    error_message  text NOT NULL DEFAULT '',
    created_at     timestamp with time zone NOT NULL,
    updated_at     timestamp with time zone NOT NULL
);

CREATE INDEX IF NOT EXISTS video_generation_jobs_attachment_idx
    ON video_generation_jobs (attachment_id) WHERE attachment_id IS NOT NULL;
