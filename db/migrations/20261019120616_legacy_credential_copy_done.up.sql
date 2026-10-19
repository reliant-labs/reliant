-- forge:backward-compatible — additive: one new single-row table that no earlier release reads or writes; no existing table changes.

-- Records that the one-time copy of the legacy credential tables (claude_auth_tokens,
-- codex_auth_tokens, copilot_auth_tokens, antigravity_auth_tokens and the LLM rows of api_keys)
-- into provider_accounts has finished. The copy runs in Go at boot (db.BootVault) because it
-- re-seals every secret under the provider_accounts AAD, which SQL cannot do; this row is what
-- stops it running again, so an account the user later deletes is never resurrected from a
-- legacy row. A follow-up migration drops the legacy tables, this table and
-- provider_accounts.legacy_source together, once every environment has booted this release.
CREATE TABLE legacy_credential_copy (
    id           integer PRIMARY KEY CHECK (id = 1),
    completed_at timestamptz NOT NULL,
    created      integer NOT NULL DEFAULT 0,
    skipped      integer NOT NULL DEFAULT 0,
    failed       integer NOT NULL DEFAULT 0
);
