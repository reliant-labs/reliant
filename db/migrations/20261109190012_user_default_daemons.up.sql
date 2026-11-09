-- forge:backward-compatible — adds a table only this release reads and writes, and keeps daemons.is_default written alongside it, so the previous release, which reads only is_default, still sees the latest choice.

-- Migration: user_default_daemons
--
-- The machine a user chose as their default used to live only on that
-- machine's daemons row (is_default). But that row is a mirror of the control
-- plane's daemon set, not the user's own data: the registry snapshot deletes a
-- row the control plane's set omits once the machine has been offline for a
-- moment, and the machine's next connect inserts it again, with is_default
-- false. A laptop's default was lost that way across sleeps and app updates,
-- and its owner had to set it again and again.
--
-- The choice is the user's, so it is recorded per user, by machine id, with no
-- foreign key to daemons: a row deleted and recreated under the same id is the
-- same machine and is still the default. A choice naming a machine that is
-- gone matches nothing, and default resolution falls back to its
-- machine-state rule, exactly as with no choice at all.
CREATE TABLE user_default_daemons (
    user_id text PRIMARY KEY,
    daemon_id text NOT NULL,
    chosen_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE user_default_daemons IS
    'The machine each user made their default (SetDefaultDaemon). Keyed by machine id with no foreign key, so it survives the registry deleting and recreating that machine''s daemons row.';

INSERT INTO user_default_daemons (user_id, daemon_id)
SELECT user_id, id FROM daemons WHERE is_default;
