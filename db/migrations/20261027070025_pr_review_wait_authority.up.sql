-- forge:backward-compatible — additive immutable per-head review authority; disabled wait adapters must be upgraded before wiring.
CREATE TABLE pr_review_wait_authorities (
    id varchar(64) PRIMARY KEY,
    binding_id varchar(255) NOT NULL REFERENCES pr_handoff_bindings(id),
    head_sha varchar(40) NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    authority_id varchar(255) NOT NULL CHECK (length(authority_id) > 0),
    policy_revision bigint NOT NULL CHECK (policy_revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (binding_id,head_sha)
);
-- Existing rows belong only to an unwired draft; blank authority is never
-- adopted or treated as authorized. No backfill guesses from provider refs.
ALTER TABLE pr_review_wait_outcomes ADD COLUMN authority_id varchar(255) NOT NULL DEFAULT '';
COMMENT ON TABLE pr_review_wait_authorities IS 'Immutable binding/head producer selection from trusted server policy; no caller or provider-ref inference; retention unresolved and app wiring disabled';
