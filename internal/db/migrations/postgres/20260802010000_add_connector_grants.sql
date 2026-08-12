-- +goose Up
--
-- Connector grants: authenticated access for third-party MCP clients (ChatGPT,
-- Claude, and their mobile apps) to run tools inside a user's cloud workspace.
--
-- A grant is the unit of consent. The security model rests on it being narrow,
-- so the schema is built to make a narrow grant the only representable one:
--
--   * daemon_id is NOT NULL. A grant is bound to exactly one daemon; there is
--     deliberately no "all my daemons" grant, because the blast radius of a
--     compromised connector should be one workspace.
--   * allowed_tools and path_root have no defaults and are checked non-empty.
--     An empty grant must mean "nothing", never "everything" — the enforcement
--     layer fails closed, and the schema agrees with it.
--   * exec_mode defaults to 'deny'. Shell access is the most dangerous thing a
--     grant can confer, so it is never the default.
--
-- These callers are driven by models reading untrusted text, so prompt
-- injection is an expected condition rather than an edge case. Enforcement
-- lives at the daemon's command dispatch (internal/daemonpolicy); this table
-- is where the decision is stored, not where it is made.

CREATE TABLE connector_grants (
    id              text PRIMARY KEY,
    user_id         text NOT NULL,

    -- The single daemon this grant may reach. ON DELETE CASCADE so deleting a
    -- workspace cannot leave a credential pointing at a recycled daemon id.
    daemon_id       text NOT NULL REFERENCES daemons(id) ON DELETE CASCADE,

    -- Human-facing label, shown in settings ("ChatGPT on my phone").
    name            text NOT NULL,

    -- Credential. Only the hash is stored; the plaintext is shown once at
    -- creation and never recoverable, matching daemon_pats. The prefix is kept
    -- separately so the UI can identify a key without holding the secret.
    token_hash      text NOT NULL UNIQUE,
    token_prefix    text NOT NULL,

    -- MCP tool names (not daemon command types). Grants are authored in the
    -- vocabulary the user sees in the consent screen; the mapping to daemon
    -- commands lives in internal/mcpserver so that translation has one home.
    allowed_tools   jsonb NOT NULL,

    -- Filesystem confinement root. Absolute path on the daemon.
    path_root       text NOT NULL,

    -- 'deny' | 'allowlist' | 'unrestricted'.
    exec_mode       text NOT NULL DEFAULT 'deny',

    -- Command basenames permitted when exec_mode = 'allowlist'.
    exec_allowlist  jsonb NOT NULL DEFAULT '[]'::jsonb,

    expires_at      timestamptz,
    last_used_at    timestamptz,
    revoked_at      timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT connector_grants_exec_mode_valid
        CHECK (exec_mode IN ('deny', 'allowlist', 'unrestricted')),

    -- A grant with no tools or no root would be a grant that the enforcement
    -- layer reads as "deny everything". That is safe but silently broken, and
    -- a user staring at a connector that refuses every call has no way to tell
    -- why. Reject it at write time instead.
    CONSTRAINT connector_grants_tools_not_empty
        CHECK (jsonb_typeof(allowed_tools) = 'array' AND jsonb_array_length(allowed_tools) > 0),
    CONSTRAINT connector_grants_path_root_not_empty
        CHECK (length(trim(path_root)) > 0),

    -- An allowlist mode with an empty allowlist denies every command while
    -- claiming to permit some. Same reasoning as above.
    CONSTRAINT connector_grants_allowlist_populated
        CHECK (
            exec_mode <> 'allowlist'
            OR (jsonb_typeof(exec_allowlist) = 'array' AND jsonb_array_length(exec_allowlist) > 0)
        )
);

-- Credential redemption is the hot path: one lookup per MCP call.
CREATE INDEX idx_connector_grants_token_hash ON connector_grants(token_hash);

-- Settings lists a user's connectors, newest first.
CREATE INDEX idx_connector_grants_user ON connector_grants(user_id, created_at DESC);

-- Revoking every grant on a daemon when a workspace is torn down.
CREATE INDEX idx_connector_grants_daemon ON connector_grants(daemon_id);

--
-- Audit log. When a third-party model runs commands in someone's workspace,
-- "what did it actually do" is the first question asked after anything goes
-- wrong, and it cannot be answered retroactively. Refused calls are recorded
-- alongside successful ones: a burst of denials is the signal that a connector
-- has been talked into trying something it shouldn't, which is precisely the
-- event worth seeing.
--
CREATE TABLE connector_audit_log (
    id            text PRIMARY KEY,

    -- Deliberately NOT a foreign key to connector_grants. Audit records must
    -- survive the revocation and deletion of the grant they describe --
    -- otherwise deleting a connector erases the evidence of what it did.
    grant_id      text NOT NULL,

    user_id       text NOT NULL,
    daemon_id     text NOT NULL,

    tool_name     text NOT NULL,
    command_type  text NOT NULL,

    -- Arguments as sent by the client. Reviewed by humans after an incident,
    -- so they are stored as given rather than summarized. NOT NULL because a
    -- call always has arguments, even if empty — "absent" and "{}" would be a
    -- distinction without a difference here, and allowing NULL only forces
    -- every reader to handle a case that never carries information.
    arguments     jsonb NOT NULL DEFAULT '{}'::jsonb,

    denied        boolean NOT NULL DEFAULT false,
    error_message text,
    duration_ms   integer,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- The two questions this table answers: "what did this connector do" and
-- "what happened in my workspace recently".
CREATE INDEX idx_connector_audit_grant ON connector_audit_log(grant_id, created_at DESC);
CREATE INDEX idx_connector_audit_user ON connector_audit_log(user_id, created_at DESC);

-- Partial index for the security-relevant read: denials are a small fraction
-- of rows, and scanning them should not require touching the whole log.
CREATE INDEX idx_connector_audit_denied ON connector_audit_log(user_id, created_at DESC)
    WHERE denied;

-- +goose Down
DROP TABLE IF EXISTS connector_audit_log;
DROP TABLE IF EXISTS connector_grants;
