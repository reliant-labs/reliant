-- forge:backward-compatible — additive future-only PR handoff tables; no existing workflow enrolls automatically.

CREATE TABLE pr_handoff_publications (
    id text PRIMARY KEY,
    owner_id varchar(255) NOT NULL,
    root_id varchar(255) NOT NULL,
    execution_id varchar(255) NOT NULL,
    chat_id varchar(255) NOT NULL,
    worktree_id varchar(255) NOT NULL,
    repo_id bigint NOT NULL CHECK (repo_id > 0),
    repo_name varchar(255) NOT NULL,
    branch varchar(255) NOT NULL,
    head_sha varchar(255) NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner_id, root_id, execution_id, repo_id, branch, head_sha)
);

CREATE TABLE pr_handoff_bindings (
    id text PRIMARY KEY,
    owner_id varchar(255) NOT NULL,
    root_id varchar(255) NOT NULL,
    execution_id varchar(255) NOT NULL,
    chat_id varchar(255) NOT NULL,
    worktree_id varchar(255) NOT NULL,
    repo_id bigint NOT NULL CHECK (repo_id > 0),
    repo_name varchar(255) NOT NULL,
    pr_number bigint NOT NULL CHECK (pr_number > 0),
    pr_object_id bigint NOT NULL CHECK (pr_object_id > 0),
    head_repo_id bigint NOT NULL CHECK (head_repo_id > 0),
    branch varchar(255) NOT NULL,
    head_sha varchar(255) NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    publication_id varchar(255) NOT NULL REFERENCES pr_handoff_publications(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner_id, repo_id, pr_number)
);

CREATE TABLE pr_handoff_reviews (
    id text PRIMARY KEY,
    binding_id varchar(255) NOT NULL REFERENCES pr_handoff_bindings(id),
    review_id varchar(255) NOT NULL,
    head_sha varchar(255) NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    verdict varchar(255) NOT NULL CHECK (verdict IN ('approved', 'changes_requested')),
    state varchar(255) NOT NULL CHECK (state IN ('pending_wait', 'needs_human', 'stale')),
    reason varchar(255) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (binding_id, review_id)
);

-- These are infrastructure records, not CRUD API entities. The ORM still
-- projects the tables; no transport endpoints or automatic enrollment exist.
COMMENT ON TABLE pr_handoff_publications IS 'Successful server-attributed git_publish receipts for opt-in PR handoff';
COMMENT ON TABLE pr_handoff_bindings IS 'Original-author PR ownership, enrolled only by authenticated PR-create receipt';
COMMENT ON TABLE pr_handoff_reviews IS 'Idempotent trusted review handoffs; pending_wait is not a delivery acknowledgement';
