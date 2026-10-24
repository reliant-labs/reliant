-- forge:backward-compatible — new internal ticket table; previous releases never read it.

-- These rows carry authority metadata, never a hosted secret or request body.
-- Retain consumed/expired rows for the lifetime of the run: the unique activity
-- key is also the durable no-reexecution tombstone after an uncertain POST.
CREATE TABLE hosted_http_action_tickets (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    temporal_run_id TEXT NOT NULL CHECK (temporal_run_id <> ''),
    activity_id TEXT NOT NULL CHECK (activity_id <> ''),
    owner_user_id TEXT NOT NULL CHECK (owner_user_id <> ''),
    project_id TEXT NOT NULL CHECK (project_id <> ''),
    workflow_name TEXT NOT NULL CHECK (workflow_name <> ''),
    node_id TEXT NOT NULL CHECK (node_id <> ''),
    grant_id TEXT NOT NULL CHECK (grant_id <> ''),
    request_sha256 TEXT NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ NOT NULL,
    redeemed_at TIMESTAMPTZ,
    UNIQUE (workflow_id, temporal_run_id, activity_id),
    CHECK (expires_at > created_at AND expires_at <= created_at + INTERVAL '1 minute')
);
COMMENT ON TABLE hosted_http_action_tickets IS 'forge:no-entity';
-- An opaque ticket is transient execution authority, not a hosted secret, but
-- still must not be enumerable by read-only query tools.
COMMENT ON COLUMN hosted_http_action_tickets.id IS 'forge:secret';
COMMENT ON COLUMN hosted_http_action_tickets.request_sha256 IS 'forge:readable — digest of a non-secret fixed HTTP request, not a credential';
CREATE INDEX hosted_http_action_tickets_workflow_id_idx ON hosted_http_action_tickets (workflow_id);

