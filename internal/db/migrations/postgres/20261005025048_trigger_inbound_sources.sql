-- +goose Up

-- Inbound trigger sources (research/INTEGRATIONS_V1_BRIEF.md §3.2).
--
-- Until now a trigger could only be a schedule. Three event-driven kinds join
-- it, each a source of the same standing instruction ("when X, run workflow W
-- in project P as user U"):
--
--   webhook         POST <PUBLIC_URL>/hooks/{trigger_id}/{token}, verified by a
--                   per-trigger token (stored hashed) or an optional HMAC
--   integration     a provider's app-level webhook or a poll, routed to every
--                   trigger whose connection covers the event's account
--   workflow_event  a run of one of the owner's workflows finished, failed or
--                   blocked (stream C implements the matcher)
--
-- Each gains a matching trigger_events kind. Widening a CHECK is replace-only;
-- NOT VALID + VALIDATE keeps the ACCESS EXCLUSIVE window to the catalog update
-- rather than a full scan of trigger_events under that lock.
ALTER TABLE triggers DROP CONSTRAINT triggers_kind_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_kind_check
    CHECK (kind IN ('schedule', 'webhook', 'integration', 'workflow_event')) NOT VALID;
ALTER TABLE triggers VALIDATE CONSTRAINT triggers_kind_check;

ALTER TABLE trigger_events DROP CONSTRAINT trigger_events_kind_check;
ALTER TABLE trigger_events ADD CONSTRAINT trigger_events_kind_check
    CHECK (kind IN ('chat.start', 'schedule', 'agent.start_run', 'builder.test',
                    'webhook', 'integration', 'workflow_event')) NOT VALID;
ALTER TABLE trigger_events VALIDATE CONSTRAINT trigger_events_kind_check;

-- An inbound event is recorded by the RECEIVER (on the api-server, which must
-- ack the sender in seconds) and launched by the WORKER. Between the two the
-- row is 'pending': the durable record that the event arrived and passed its
-- filter, which the fire activity later settles to launched/skipped/failed.
-- A pending row whose fire never started is redriven, so a sender that does
-- not retry (GitHub) still gets exactly one run.
ALTER TABLE trigger_events DROP CONSTRAINT trigger_events_outcome_check;
ALTER TABLE trigger_events ADD CONSTRAINT trigger_events_outcome_check
    CHECK (outcome IN ('pending', 'launched', 'skipped', 'failed')) NOT VALID;
ALTER TABLE trigger_events VALIDATE CONSTRAINT trigger_events_outcome_check;

CREATE INDEX idx_trigger_events_pending ON trigger_events (created_at) WHERE outcome = 'pending';

-- filter is a CEL bool over the `trigger` root (trigger.payload.*), the same
-- root every node sees. Empty matches every event. Validated at write time;
-- evaluated when an event arrives, and a miss is recorded as 'skipped'.
ALTER TABLE triggers ADD COLUMN filter text NOT NULL DEFAULT '';

-- connection_id is the connection an integration trigger listens through. It
-- is the routing key: an app-level event reaches a trigger only when the
-- trigger owner's connection covers the event's account. A column rather than
-- config so routing can join it; SET NULL because a deleted connection leaves
-- the trigger unroutable (and visible as such) rather than silently gone.
ALTER TABLE triggers ADD COLUMN connection_id text REFERENCES connections(id) ON DELETE SET NULL;
CREATE INDEX idx_triggers_connection ON triggers (connection_id) WHERE connection_id IS NOT NULL;

-- A webhook trigger's credentials. The token is server-generated with 256 bits
-- of entropy, so a plain SHA-256 is the right hash: it is compared in constant
-- time and shown to the owner exactly once. The optional HMAC secret must be
-- recoverable (it keys a MAC), so it is SEALED by the vault under the owner's
-- tenant, never stored in the clear. Both live in columns rather than config,
-- because config is rendered back to clients and these must never be.
ALTER TABLE triggers ADD COLUMN webhook_token_hash bytea;
ALTER TABLE triggers ADD COLUMN webhook_secret_sealed bytea;

-- App-level routing reads every enabled integration trigger of one provider.
CREATE INDEX idx_triggers_integration ON triggers ((config ->> 'integration'))
    WHERE kind = 'integration' AND enabled;

-- trigger_registrations is a trigger's state AT THE PROVIDER: the poll cursor
-- for a polled source, or the hook id a per-trigger webhook registration
-- returned. One row per trigger, created by the first poll (the baseline) or
-- registration, and removed with the trigger.
CREATE TABLE trigger_registrations (
    trigger_id      text PRIMARY KEY REFERENCES triggers(id) ON DELETE CASCADE,
    provider        text NOT NULL,
    registration_id text NOT NULL DEFAULT '',
    cursor          text NOT NULL DEFAULT '',
    last_polled_at  timestamptz,
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'error')),
    status_detail   text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
