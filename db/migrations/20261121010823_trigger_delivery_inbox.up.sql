-- forge:backward-compatible — adds a private inbox; previous releases ignore it and continue reading and writing trigger_events as before.

CREATE TABLE trigger_delivery_inbox (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL CHECK (source IN ('integration', 'webhook', 'poll')),
    provider TEXT NOT NULL DEFAULT '',
    scope_key TEXT NOT NULL DEFAULT '',
    delivery_key TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    -- Only normalized application payload and verified routing evidence. Never
    -- request credentials/authentication material. Cleared on completion.
    payload JSONB,
    available_at TIMESTAMPTZ NOT NULL,
    lease_token TEXT,
    lease_until TIMESTAMPTZ,
    failures INTEGER NOT NULL DEFAULT 0 CHECK (failures >= 0),
    completed_at TIMESTAMPTZ,
    dead_at TIMESTAMPTZ,
    -- Stable diagnostic code, never provider payloads or upstream errors.
    failure_code TEXT NOT NULL DEFAULT '',
    -- The legacy /hooks response can return the real event on a redelivery.
    event_id TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL DEFAULT '',
    UNIQUE (source, provider, scope_key, delivery_key),
    CHECK ((lease_token IS NULL) = (lease_until IS NULL)),
    CHECK (completed_at IS NULL OR (payload IS NULL AND lease_until IS NULL AND dead_at IS NULL)),
    CHECK (dead_at IS NULL OR lease_until IS NULL),
    CHECK (completed_at IS NOT NULL OR payload IS NOT NULL)
);

COMMENT ON TABLE trigger_delivery_inbox IS 'forge:no-entity';
COMMENT ON COLUMN trigger_delivery_inbox.payload IS 'forge:secret';
COMMENT ON COLUMN trigger_delivery_inbox.delivery_key IS 'forge:secret';
COMMENT ON COLUMN trigger_delivery_inbox.lease_token IS 'forge:secret';

CREATE INDEX trigger_delivery_inbox_available_idx ON trigger_delivery_inbox (available_at, received_at, id)
    WHERE completed_at IS NULL AND dead_at IS NULL;
CREATE INDEX trigger_delivery_inbox_completed_idx ON trigger_delivery_inbox (completed_at) WHERE completed_at IS NOT NULL;
CREATE INDEX trigger_delivery_inbox_dead_idx ON trigger_delivery_inbox (dead_at) WHERE dead_at IS NOT NULL;
