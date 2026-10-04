-- +goose Up

-- Triggers are standing instructions to start a run without a human typing:
-- "every weekday at 9am, run workflow W in project P with prompt M". Until now
-- every run began with a request carrying a user's JWT, so "who does this run
-- as" was never a stored fact. A scheduled fire has no request, so the owner
-- has to live on the row — user_id is both the owner for listing and the
-- identity the launched run executes as.
--
-- See research/TRIGGERS.md for the model.
CREATE TABLE triggers (
    id TEXT PRIMARY KEY,
    -- The owner, AND the identity every run this trigger launches executes as.
    user_id TEXT NOT NULL,
    -- A trigger is meaningless without the project it runs in, so the project
    -- going away takes the trigger with it rather than leaving a row that can
    -- only ever fail to fire.
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- Nullable: NULL means the project's main worktree. A deleted worktree
    -- falls back to that default rather than deleting the trigger — the
    -- standing instruction is still valid, it just runs somewhere else.
    worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    name TEXT NOT NULL,
    -- Only 'schedule' today. Webhook and GitHub kinds become new values here
    -- plus new Config shapes; a typo in application code should fail the write
    -- rather than create a kind nothing knows how to fire.
    kind TEXT NOT NULL CHECK (kind IN ('schedule')),
    -- Disabled triggers stay in the table: the Temporal Schedule is PAUSED
    -- rather than deleted, so enabling is a one-field write and the firing
    -- history survives.
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    workflow TEXT NOT NULL,
    -- presets and params are the same launch inputs an interactive start
    -- carries, frozen at definition time. jsonb, not text, so a malformed
    -- value fails at write rather than at fire time, hours later, unattended.
    presets JSONB NOT NULL DEFAULT '{}'::JSONB,
    params JSONB NOT NULL DEFAULT '{}'::JSONB,
    -- The seed prompt every launched run starts from. Defaulted rather than
    -- nullable: "no prompt" is the empty string, and readers should not have
    -- to distinguish it from NULL.
    message TEXT NOT NULL DEFAULT '',
    -- Kind-specific source config (ScheduleConfig for kind='schedule'). Kept
    -- opaque on purpose: each kind's knobs are its own, and promoting them to
    -- columns would make every new kind a migration.
    config JSONB NOT NULL DEFAULT '{}'::JSONB,
    -- TIMESTAMPTZ, not TIMESTAMP: 20260728000000_timestamps_to_timestamptz.sql
    -- converted every existing column, so a bare TIMESTAMP here would be the
    -- only naive column in the schema.
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Names are how a human refers to a trigger ("pause the nightly audit"),
    -- and how the CLI addresses one. Unique per user per project so that
    -- reference resolves to exactly one row.
    UNIQUE (user_id, project_id, name)
);

-- Listing is always scoped one of two ways: everything I own (the triggers UI
-- and the schedule syncer's startup reconciliation) or everything in this
-- project (the project view).
CREATE INDEX idx_triggers_user ON triggers(user_id);
CREATE INDEX idx_triggers_project ON triggers(project_id);

-- A trigger_events row is the durable record of INTENT to launch, written
-- before the Temporal start. That ordering is what makes the start safe to
-- retry: the workflow id is deterministic (it is the chat id), so a retry
-- attaches to the existing run instead of creating a second one.
--
-- Events are wider than triggers: an interactive chat start is an event of
-- kind 'chat.start' with no stored definition behind it, which is why
-- trigger_id is nullable rather than the table's parent.
CREATE TABLE trigger_events (
    id TEXT PRIMARY KEY,
    -- NULL for ad hoc kinds ('chat.start'), and NULL once the trigger that
    -- fired is deleted. SET NULL rather than CASCADE because the firing
    -- history is evidence about chats that still exist — deleting a trigger
    -- must not erase the record of the runs it started.
    trigger_id TEXT REFERENCES triggers(id) ON DELETE SET NULL,
    -- Denormalized from the trigger (or the requesting user, for chat.start)
    -- so an event survives its trigger's deletion still knowing whose it was.
    user_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('chat.start', 'schedule')),
    dedupe_key TEXT NOT NULL,
    -- When the firing was FOR, which is not when the row was written: a
    -- scheduled fire replayed after an outage occurred at its scheduled time.
    occurred_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::JSONB,
    outcome TEXT NOT NULL CHECK (outcome IN ('launched', 'skipped', 'failed')),
    outcome_detail TEXT NOT NULL DEFAULT '',
    -- The chat this firing launched, when it launched one. SET NULL so
    -- deleting a chat does not delete the record that something started it.
    chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- The constraint the whole design leans on. "A chat starts exactly once"
    -- and "a retried scheduled fire cannot launch twice" are both this one
    -- index: chat.start dedupes on the chat id, schedule on the Temporal
    -- fire-workflow id (unique per scheduled time). Enforced by the database
    -- rather than by client discipline, because the clients are a web app, a
    -- CLI, and a Temporal activity that retries on its own.
    UNIQUE (kind, dedupe_key)
);

-- The trigger detail view asks exactly one question: the recent firings of
-- this trigger, newest first. DESC in the index so that is an ordered scan
-- rather than a sort.
CREATE INDEX idx_trigger_events_trigger_occurred ON trigger_events(trigger_id, occurred_at DESC);
-- The reverse lookup: given a chat, what started it. Used to label automation
-- runs in the UI and to resolve a half-launched event back to its session.
CREATE INDEX idx_trigger_events_chat ON trigger_events(chat_id);
