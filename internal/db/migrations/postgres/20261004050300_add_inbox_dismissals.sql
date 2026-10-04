-- +goose Up

-- G9 (WORKFLOW_UI.md §13): the Inbox lists everything waiting on a user. Only
-- failure items can be dismissed; approvals and questions clear when resolved.
--
-- item_id is the Inbox's stable item key. Failure item ids embed the id of the
-- newest failing event, so a NEWER failure has a new id and the dismissal of an
-- older one does not hide it.
CREATE TABLE IF NOT EXISTS inbox_dismissals (
    user_id      TEXT        NOT NULL,
    item_id      TEXT        NOT NULL,
    dismissed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, item_id)
);

-- The Inbox reads only PENDING approvals and questions. The existing indexes
-- are on chat_id / thread_id / (questions) status in full; these partial
-- indexes hold just the handful of pending rows, so the cross-chat read never
-- scans resolved history.
CREATE INDEX IF NOT EXISTS idx_approvals_pending ON approvals (chat_id) WHERE status = 1;
CREATE INDEX IF NOT EXISTS idx_questions_pending ON questions (chat_id) WHERE status = 1;

-- Serves the "newest failed firing per trigger" lookup.
CREATE INDEX IF NOT EXISTS idx_trigger_events_failed ON trigger_events (trigger_id, occurred_at DESC, id DESC) WHERE outcome = 'failed';
