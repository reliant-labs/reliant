-- forge:backward-compatible — additive attention bookkeeping tables; older releases never read them.

-- Retain resolved/empty tombstones: their high-water actor fence prevents a
-- delayed old completion from opening a new episode. Nanoseconds preserve the
-- immutable Temporal ordering that timestamptz's microseconds cannot represent.
CREATE TABLE attention_incidents (
    user_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    incident_key TEXT NOT NULL CHECK (length(incident_key) > 0 AND octet_length(incident_key) <= 512),
    episode_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '' CHECK (state IN ('', 'recovering', 'action_required', 'resolved')),
    chat_id TEXT NOT NULL DEFAULT '',
    trigger_id TEXT NOT NULL DEFAULT '',
    failure_class TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    opened_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
    actor_run_id TEXT NOT NULL DEFAULT '',
    actor_started_at_ns BIGINT NOT NULL DEFAULT 0,
    owner_run_id TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    notified BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (user_id, project_id, incident_key),
    CHECK ((state = 'recovering') = (lease_until IS NOT NULL)),
    CHECK (state = '' OR episode_id <> '')
);
COMMENT ON TABLE attention_incidents IS 'forge:no-entity';
CREATE INDEX attention_incidents_open_user ON attention_incidents (user_id, opened_at, episode_id)
    WHERE state IN ('recovering', 'action_required');

-- Durable per-execution receipts, never deleted at episode resolution. A stale
-- but valid registration still suppresses that chat's legacy streak alerts.
-- No chat/project FKs: deleting/reassigning a chat must not remove the fence;
-- every read and write checks live stored ownership instead.
CREATE TABLE attention_actors (
    run_id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    incident_key TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    started_at_ns BIGINT NOT NULL,
    start_seen BOOLEAN NOT NULL DEFAULT false,
    start_state TEXT NOT NULL DEFAULT '' CHECK (start_state IN ('', 'recovering', 'action_required')),
    finished BOOLEAN NOT NULL DEFAULT false,
    expired BOOLEAN NOT NULL DEFAULT false,
    recovery_timeout_ns BIGINT NOT NULL DEFAULT 0
);
COMMENT ON TABLE attention_actors IS 'forge:no-entity';
CREATE INDEX attention_actors_chat ON attention_actors (user_id, project_id, chat_id);

