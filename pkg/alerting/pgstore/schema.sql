-- Alerting state and history. Idempotent. {{schema}} is replaced by the schema
-- name plus a dot (or nothing): see pgstore.SchemaSQL.
--
-- alert_instances is the CURRENT state of every label set of every rule that is
-- pending, firing, or resolved-but-not-yet-delivered. alert_events is the
-- append-only history of transitions.

CREATE TABLE IF NOT EXISTS {{schema}}alert_instances (
    owner       TEXT        NOT NULL,
    env          TEXT        NOT NULL,
    rule         TEXT        NOT NULL,
    fingerprint  TEXT        NOT NULL,
    labels       JSONB       NOT NULL,
    annotations  JSONB       NOT NULL DEFAULT '{}'::jsonb,
    state        TEXT        NOT NULL,
    active_at    TIMESTAMPTZ NOT NULL,
    firing_at    TIMESTAMPTZ,
    cleared_at   TIMESTAMPTZ,
    resolved_at  TIMESTAMPTZ,
    resolve_sent BOOLEAN     NOT NULL DEFAULT FALSE,
    last_eval_at TIMESTAMPTZ NOT NULL,
    last_value   DOUBLE PRECISION NOT NULL,
    why          JSONB,
    PRIMARY KEY (owner, env, rule, fingerprint),
    CONSTRAINT ck_alert_instances_state CHECK (state IN ('pending', 'firing', 'resolved'))
);

CREATE INDEX IF NOT EXISTS idx_alert_instances_owner_state ON {{schema}}alert_instances (owner, state);

CREATE TABLE IF NOT EXISTS {{schema}}alert_events (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner      TEXT        NOT NULL,
    env         TEXT        NOT NULL,
    rule        TEXT        NOT NULL,
    fingerprint TEXT        NOT NULL,
    labels      JSONB       NOT NULL,
    transition  TEXT        NOT NULL,
    value       DOUBLE PRECISION NOT NULL,
    at          TIMESTAMPTZ NOT NULL,
    why         JSONB,
    CONSTRAINT ck_alert_events_transition CHECK (transition IN ('pending', 'firing', 'resolved', 'inactive', 'removed'))
);

CREATE INDEX IF NOT EXISTS idx_alert_events_owner_at ON {{schema}}alert_events (owner, at DESC);
CREATE INDEX IF NOT EXISTS idx_alert_events_rule_at ON {{schema}}alert_events (owner, env, rule, at DESC);

-- why is the structured why-fired payload (alerting.Why), not part of an
-- alert's identity. Added after the first release of this schema, so a database
-- created before it gains the column here.
ALTER TABLE {{schema}}alert_instances ADD COLUMN IF NOT EXISTS why JSONB;
ALTER TABLE {{schema}}alert_events ADD COLUMN IF NOT EXISTS why JSONB;
