-- forge:backward-compatible — adds a table only this release reads and writes; the previous release never names it, so after a rollback the links simply stop growing.

-- Migration: chat_pr_links
-- Created: 2026-11-24T17:08:18Z
--
-- Which pull requests a chat produced (dev-docs/design/agent-analytics.md
-- §3.2, decision 10): the join behind cost per merged PR. pr_records mirrors
-- the pull requests themselves and names no chat; a worktree's branch is a
-- creation-time label, not a source of truth. So the link is written where
-- the pull request is made, and inferred from the branch only as a fallback:
--
--   - worktree_pr: CreateWorktreePR opened it from the chat's worktree;
--   - integration: a run of the chat opened it through the GitHub
--     integration (a workflow's github/pr.create step, or the agent's
--     github__pr_create tool), recorded from the integration write ledger;
--   - head_ref: a pr_records row first appeared whose head branch and
--     repository match exactly one worktree, and no explicit link names the
--     pull request.
--
-- Explicit beats inferred: an explicit link replaces the inferred one for the
-- same chat and removes the pull request's inferred links to other chats, and
-- a pull request with an explicit link gets no inferred ones.
--
-- A link names the pull request by provider, repository (owner/name,
-- lowercased) and number, which is what every writer knows. repository_id is
-- the provider's stable id once known (from pr_records, filled by whichever
-- of the link and the record arrives second); '' until then.
--
-- user_id and project_id are the chat's owner and project when the link was
-- written. There is no foreign key: account purge (internal/accountpurge) and
-- project delete (internal/store/project_delete.go) delete these rows by hand.
-- A writer selects the chat FOR KEY SHARE, so a link cannot land on a chat a
-- purge is deleting (the chat_write_admission protocol of
-- 20261114221537_chat_purge_write_admission).
--
-- Facts only, no content: no title, body, branch name or URL.
CREATE TABLE chat_pr_links (
    id text PRIMARY KEY,
    chat_id text NOT NULL,
    user_id text NOT NULL,
    project_id text NOT NULL,
    -- The run (workflows.id) whose step opened it; NULL when no run did.
    run_id text,
    provider text NOT NULL CHECK (provider IN ('github')),
    repository text NOT NULL CHECK (repository <> '' AND repository = lower(repository)),
    repository_id text NOT NULL DEFAULT '',
    pr_number integer NOT NULL CHECK (pr_number > 0),
    source text NOT NULL CHECK (source IN ('worktree_pr', 'integration', 'head_ref')),
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    UNIQUE (chat_id, provider, repository, pr_number)
);

COMMENT ON TABLE chat_pr_links IS 'forge:no-entity';

-- The pull request's links, for explicit-beats-inferred and the record join.
CREATE INDEX chat_pr_links_pr_idx ON chat_pr_links (provider, repository, pr_number);
CREATE INDEX chat_pr_links_repository_id_idx ON chat_pr_links (provider, repository_id, pr_number) WHERE repository_id <> '';
-- Account purge and project delete.
CREATE INDEX chat_pr_links_user_idx ON chat_pr_links (user_id);
CREATE INDEX chat_pr_links_project_idx ON chat_pr_links (project_id);
