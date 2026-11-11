-- forge:backward-compatible — adds a table only this release reads and writes; the previous release never reads it, so after a rollback remove/reclone and workspace creation behave as they did before (unguarded), and the rows are inert.

-- Migration: checkout_teardowns
--
-- RemoveProjectFromDaemon and RecloneProjectOnDaemon delete a project's
-- checkout on a machine, and every branch workspace of the project there is a
-- git worktree of that checkout. The machine keeps deleting after the server
-- stops waiting (a timeout, a server restart), so the server must not let a
-- workspace be created there until it KNOWS the delete is over
-- (reliant-labs/reliant#2429, design 1).
--
-- A row is one teardown: recorded before the command is sent, carrying the id
-- the machine journals before it deletes anything and acknowledges by. While
-- a row is pending, workspace creation on that checkout is refused; a retry of
-- the same remove/reclone reuses the row (the machine dedupes by id), and a
-- status query settles it (done, failed, or not_run when the machine never
-- received it and has now fenced the id so a late delivery cannot run). A
-- machine too old to acknowledge is released expired after a grace period.
CREATE TABLE checkout_teardowns (
    id text PRIMARY KEY,
    project_id text NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    daemon_id text NOT NULL REFERENCES daemons (id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('remove', 'reclone')),
    path text NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done', 'failed', 'not_run', 'expired')),
    detail text NOT NULL DEFAULT '',
    created_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    settled_at timestamp with time zone
);

-- At most one unsettled teardown per checkout: a second remove/reclone waits
-- for (or reuses) the first, so two deletes of one path never race.
CREATE UNIQUE INDEX checkout_teardowns_one_pending ON checkout_teardowns (project_id, daemon_id) WHERE state = 'pending';
CREATE INDEX checkout_teardowns_daemon ON checkout_teardowns (daemon_id);

COMMENT ON TABLE checkout_teardowns IS
    'A remove or reclone of a project''s checkout on a machine, by the id the machine acknowledges; a pending row holds back workspace creation on that checkout.';
COMMENT ON COLUMN checkout_teardowns.state IS
    'pending: outcome unknown (creation held); done/failed: the machine reported it; not_run: the machine never ran it and fenced the id; expired: a machine too old to acknowledge, released after the grace period.';
