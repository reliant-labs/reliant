-- forge:backward-compatible — additive disabled original-author review wait subscription and reference-only inbox.

CREATE TABLE pr_review_wait_subscriptions (
    id varchar(255) PRIMARY KEY,
    binding_id varchar(255) NOT NULL REFERENCES pr_handoff_bindings(id),
    head_sha varchar(40) NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    workflow_id varchar(255) NOT NULL,
    original_run_id varchar(255) NOT NULL,
    current_run_id varchar(255) NOT NULL,
    state varchar(32) NOT NULL CHECK (state IN ('armed', 'resolved', 'cancelled', 'needs_human')),
    ready boolean NOT NULL DEFAULT false,
    notified_sequence bigint NOT NULL DEFAULT 0,
    consumed_sequence bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (binding_id, head_sha)
);

CREATE TABLE pr_review_wait_outcomes (
    id varchar(255) PRIMARY KEY,
    binding_id varchar(255) NOT NULL REFERENCES pr_handoff_bindings(id),
    head_sha varchar(40) NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    provider_ref varchar(255) NOT NULL,
    provider_sequence bigint NOT NULL CHECK (provider_sequence > 0),
    state varchar(32) NOT NULL CHECK (state IN ('approved', 'changes', 'closed', 'unavailable')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (binding_id, head_sha, provider_ref),
    UNIQUE (binding_id, head_sha, provider_sequence)
);
CREATE INDEX pr_review_wait_outcome_latest ON pr_review_wait_outcomes(binding_id, head_sha, provider_sequence DESC);

COMMENT ON TABLE pr_review_wait_subscriptions IS 'Exact-author durable PR review wait; notification ack is not consumption; disabled until retention and trusted event admission are wired';
COMMENT ON TABLE pr_review_wait_outcomes IS 'Provider-authorized reference-only inbox; no finding text and no arrival-order authority';
