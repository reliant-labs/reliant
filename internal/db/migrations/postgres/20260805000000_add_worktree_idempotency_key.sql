-- +goose Up
-- Worktree creation becomes asynchronous: CreateWorktree writes a CREATING row
-- and returns, then does the 30-120s daemon work on a context detached from the
-- request. That closes an existing bug where a client disconnect cancelled the
-- inbound context and aborted the work mid-flight — and, worse, cancelled the
-- rollback that was supposed to clean up the half-created state, leaving
-- orphaned directories on the daemon.
--
-- Async creation introduces a retry hazard that the synchronous version did not
-- have. Once the response no longer waits for completion, a client that loses
-- the reply cannot distinguish "creation failed" from "creation succeeded and
-- the response was dropped". The natural retry then produces a second workspace
-- with a second on-disk checkout. This is not hypothetical on mobile, where the
-- app is backgrounded routinely mid-request.
--
-- idempotency_key lets a retry present the same key and get the original row
-- back. Scoped per project rather than globally: the key is client-generated,
-- and two projects must be able to use the same key without colliding.
--
-- Nullable with a partial unique index rather than NOT NULL: existing rows have
-- no key, and callers that do not retry (the desktop UI today) have no reason
-- to send one. A plain UNIQUE constraint would treat every NULL as distinct in
-- Postgres, which happens to be the behaviour we want, but the partial index
-- states the intent and keeps the index small.
ALTER TABLE worktrees ADD COLUMN idempotency_key text;

CREATE UNIQUE INDEX idx_worktrees_idempotency_key
    ON worktrees (project_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_worktrees_idempotency_key;
ALTER TABLE worktrees DROP COLUMN IF EXISTS idempotency_key;
