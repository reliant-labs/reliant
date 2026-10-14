-- +goose Up

-- The write ledger: one row per MUTATING call an integration action makes on a
-- run's behalf (a GitHub comment, review, label, PR, ...), written BEFORE the
-- request goes out and completed after.
--
-- It exists so an event that arrives later can be attributed. GitHub sends the
-- webhook for a write within moments, often before our own response has been
-- read; the intent row therefore commits first, and "the webhook beat the
-- response" is not a race. An arriving event is matched against it by object id
-- (exact) or by target + content hash inside a short window (intent), and that
-- match is what becomes trigger.origin.
--
-- Not a foreign key on chat_id or trigger_id: the ledger is an audit trail that
-- outlives the chat it describes (retention is by created_at), and a chat
-- delete must never be blocked by, or cascade into, it.
CREATE TABLE IF NOT EXISTS integration_writes (
    id           text        PRIMARY KEY,
    -- The run owner: whose credential the call used.
    user_id      text        NOT NULL,
    chat_id      text        NOT NULL DEFAULT '',
    -- The ROOT workflow of that chat (what a trigger's `workflow` names), not
    -- the sub-workflow the node happened to run in.
    workflow     text        NOT NULL DEFAULT '',
    -- The trigger row that launched the writing run, when one did, and the
    -- name a workflow knows it by (the declared trigger's name), frozen here so
    -- history still reads after a rename.
    trigger_id   text        NOT NULL DEFAULT '',
    trigger_name text        NOT NULL DEFAULT '',
    node_id      text        NOT NULL DEFAULT '',
    tool_call_id text        NOT NULL DEFAULT '',
    integration  text        NOT NULL,
    action       text        NOT NULL,
    -- The connection the call authenticated with, and whose identity that is:
    -- 'owner' (the person's own token) or 'app' (a GitHub App installation).
    connection_id text       NOT NULL DEFAULT '',
    actor        text        NOT NULL DEFAULT 'owner',
    -- The container the write lands in: 'github:owner/repo'. Indexed, because a
    -- creation's own target does not exist yet when its webhook arrives.
    scope        text        NOT NULL,
    -- What was written: 'github:owner/repo#N' | 'github:owner/repo@branch', or
    -- the scope itself until a creation's response names the new object.
    target       text        NOT NULL,
    -- What an echo would carry: {object_kind, events, body_sha256, label,
    -- head_sha, branch, review_state, ...}. Names are internal/integrations/provenance's.
    correlation  jsonb       NOT NULL DEFAULT '{}'::jsonb,
    object_id    text        NOT NULL DEFAULT '',
    object_url   text        NOT NULL DEFAULT '',
    status       text        NOT NULL,
    -- Set on a failed write whose outcome is unknown (a timeout, a 5xx): the
    -- provider may have applied it, so its echo still counts as ours. A definite
    -- rejection (a 4xx) is not ambiguous.
    ambiguous    boolean     NOT NULL DEFAULT false,
    error        text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT integration_writes_status_check CHECK (status IN ('intent', 'done', 'failed')),
    CONSTRAINT integration_writes_actor_check CHECK (actor IN ('owner', 'app'))
);

-- Exact lookups: an event's object id against the object a write created.
CREATE INDEX IF NOT EXISTS idx_integration_writes_object
    ON integration_writes (integration, scope, object_id) WHERE object_id <> '';

-- Intent lookups: recent writes into one repository, newest first.
CREATE INDEX IF NOT EXISTS idx_integration_writes_scope_recent
    ON integration_writes (integration, scope, created_at DESC);

-- Retention.
CREATE INDEX IF NOT EXISTS idx_integration_writes_created
    ON integration_writes (created_at);
