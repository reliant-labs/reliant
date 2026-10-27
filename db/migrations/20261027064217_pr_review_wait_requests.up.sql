-- forge:backward-compatible — additive cancellation tombstones for disabled review waits; previous code ignores request ownership.
CREATE TABLE pr_review_wait_requests (
    id varchar(64) PRIMARY KEY,
    owner_id varchar(255) NOT NULL,
    chat_id varchar(255) NOT NULL,
    workflow_id varchar(255) NOT NULL,
    run_id varchar(255) NOT NULL,
    state varchar(16) NOT NULL CHECK (state IN ('arming','armed','cancelled')),
    subscription_id varchar(255),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE pr_review_wait_subscriptions ADD COLUMN request_key varchar(64) NOT NULL DEFAULT '';
CREATE INDEX pr_review_wait_requests_author ON pr_review_wait_requests(workflow_id,run_id);
COMMENT ON TABLE pr_review_wait_requests IS 'Exact author/node-occurrence arm idempotency and pre-result cancellation tombstones; metadata only; disabled pending retention policy';
