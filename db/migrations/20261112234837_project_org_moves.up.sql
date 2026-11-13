-- forge:backward-compatible — a new table no previous release reads or writes.

-- Migration: project_org_moves
-- Created: 2026-11-12T23:48:37Z
--
-- The ledger of moving a project to another org (control-plane
-- docs/design/org-project-move.md §3.4; owner decision 2026-11-12). One row
-- per requested move. It is the durable record that drives the move's saga
-- (its id is the ProjectOrgMove Temporal workflow's id and the key of every
-- control-plane step), the idempotency record of the request, and the audit
-- read of moves in and out of an org.
--
--   preparing  the row exists; control-plane is revoking the source org's
--              per-project state (PrepareProjectOrgMove)
--   prepared   control-plane is done; reliant's transaction may commit
--   moved      reliant's transaction committed (store.MoveProjectOrg): the
--              point of no return. counts holds what it changed.
--   completed  every roll-forward step is done (activity, trigger re-enables)
--   aborting   a step before the commit failed; control-plane is restoring
--   aborted    restored; detail says why
--
-- No foreign keys: the history outlives the project, and org and user ids are
-- control-plane's. owner_user_id is the project's owner, whose org lock the
-- commit takes; actor_user_id is who asked (the owner, in v1).
--
-- At most one move of a project is open at a time: project_org_moves_one_open.
-- The table is new and empty, so its indexes are built here, in the
-- transaction that creates it, rather than CONCURRENTLY in files of their own:
-- nothing can read or write it before this commits, and a unique index that
-- exists from the table's first instant has no window in which two open moves
-- could be inserted.
CREATE TABLE project_org_moves (
    id text PRIMARY KEY CHECK (id <> ''),
    project_id text NOT NULL CHECK (project_id <> ''),
    owner_user_id text NOT NULL CHECK (owner_user_id <> ''),
    actor_user_id text NOT NULL CHECK (actor_user_id <> ''),
    from_org_id text NOT NULL CHECK (from_org_id <> ''),
    to_org_id text NOT NULL CHECK (to_org_id <> '' AND to_org_id <> from_org_id),
    request_key text NOT NULL CHECK (request_key <> ''),
    state text NOT NULL CHECK (state IN ('preparing', 'prepared', 'moved', 'completed', 'aborting', 'aborted')),
    detail text NOT NULL DEFAULT '',
    counts jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamp with time zone NOT NULL DEFAULT NOW(),
    updated_at timestamp with time zone NOT NULL DEFAULT NOW(),
    CONSTRAINT project_org_moves_request_key UNIQUE (actor_user_id, request_key)
);
COMMENT ON TABLE project_org_moves IS
    'One row per move of a project to another org (control-plane docs/design/org-project-move.md §3.4): the saga''s state, the request''s idempotency key, and the ledger an org''s admins read.';
COMMENT ON COLUMN project_org_moves.counts IS
    'What the move changed, written when it commits (moved) and completed later: chats, runs, triggers, triggers_paused, triggers_reenabled, shares_cleared, bindings_cleared, secret_entries_revoked.';

-- One open move per project: a second request while one is in flight is
-- refused by the database, whichever replica takes it.
CREATE UNIQUE INDEX project_org_moves_one_open ON project_org_moves (project_id)
    WHERE state IN ('preparing', 'prepared', 'moved');

-- An org's ledger lists the moves out of it and into it, newest first.
CREATE INDEX project_org_moves_from_org ON project_org_moves (from_org_id, created_at DESC);
CREATE INDEX project_org_moves_to_org ON project_org_moves (to_org_id, created_at DESC);
