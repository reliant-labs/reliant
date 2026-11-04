-- forge:backward-compatible — a new table and new nullable/defaulted columns; the previous release never reads or writes them, and a pool-bound trigger it fires after a rollback names no daemon, so it fails loudly as "trigger's daemon no longer exists" instead of running anywhere.

-- Machine pools: a named set of the owner's machines that a trigger (or a
-- workflow's `daemon: {pool: …}`) targets instead of one machine. Each launch
-- is placed on one member and stays pinned to it (dev-docs/design/machine-pools.md).

-- A machine's durable labels, set by its owner. Pool membership is a label
-- match, and it must hold while the machine sleeps (a pool wakes suspended
-- members), so it cannot come from the labels a daemon reports when it
-- connects. Registration and identity upserts name their columns, so neither
-- touches this one.
ALTER TABLE daemons ADD COLUMN labels jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE machine_pools (
    id text PRIMARY KEY,
    -- The owner. Machines are user-owned, so a pool is too: its members are
    -- the owner's machines whose labels match selector.
    user_id text NOT NULL,
    name text NOT NULL,
    -- Label map a member's labels must contain (every key, equal value).
    selector jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Size bounds. min is advisory (shown, warned when unmet); max bounds a
    -- scale-up (0: the pool never grows by itself).
    min_members integer NOT NULL DEFAULT 0,
    max_members integer NOT NULL DEFAULT 0,
    -- The size a scale-up creates a member at; '' when the pool never scales.
    member_size text NOT NULL DEFAULT '',
    -- At most this many live runs placed by the pool on one member (0: no limit).
    max_runs_per_member integer NOT NULL DEFAULT 0,
    -- What a full pool does. 'never' queues the run. A new member bills the
    -- owner, so growing is a later, approval-gated value ('ask': design
    -- Stage 4), added with the code that honours it.
    scale_up text NOT NULL DEFAULT 'never',
    -- How long a member may sit idle before it sleeps (a Go duration). A CI
    -- pool's members wake per burst of work; the machine default (30m) keeps
    -- an unused 2xl running long after its last job, so pools default to 10m.
    member_idle_timeout text NOT NULL DEFAULT '10m',
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT machine_pools_user_name_key UNIQUE (user_id, name),
    CONSTRAINT machine_pools_name_present CHECK (length(trim(name)) > 0),
    CONSTRAINT machine_pools_selector_present CHECK (selector <> '{}'::jsonb),
    CONSTRAINT machine_pools_bounds CHECK (min_members >= 0 AND max_members >= 0 AND max_runs_per_member >= 0 AND (max_members = 0 OR min_members <= max_members)),
    CONSTRAINT machine_pools_scale_up CHECK (scale_up IN ('never'))
);

-- The pool that placed a chat's run; with chats.active_daemon_id (the member)
-- it is which member ran what, and it is how a member's live runs are counted.
ALTER TABLE chats ADD COLUMN machine_pool_id text REFERENCES machine_pools (id) ON DELETE SET NULL;
CREATE INDEX chats_machine_pool_member ON chats (machine_pool_id, active_daemon_id) WHERE machine_pool_id IS NOT NULL;

-- A trigger runs on one machine (daemon_id), on none (no_machine), or on a
-- pool's members (machine_pool_id): never two of them. A pool still targeted
-- by a trigger cannot be deleted.
ALTER TABLE triggers ADD COLUMN machine_pool_id text REFERENCES machine_pools (id) ON DELETE RESTRICT;
ALTER TABLE triggers ADD CONSTRAINT triggers_pool_names_no_daemon_check
    CHECK (machine_pool_id IS NULL OR (daemon_id IS NULL AND NOT no_machine));
