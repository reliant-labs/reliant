-- forge:backward-compatible — adds a table nothing reads yet; the previous release never touches it, and daemons.is_default stays the one column every release reads and writes.

-- Migration: user_default_daemons
--
-- The expand half of making a user's default machine survive the registry
-- recreating its row.
--
-- The default lives on the machine's daemons row (is_default). That row is a
-- mirror of the control plane's daemon set: the registry snapshot deletes a
-- row the set omits once the machine has been offline for a moment, and the
-- machine's next connect inserts it again with is_default false, so a laptop's
-- default is lost across sleeps and app updates.
--
-- The next release remembers the choice here, by machine id with no foreign
-- key, so it outlives the row, and restores is_default when the row returns.
-- A choice that outlives its row must also be forgettable while the row is
-- gone: a clear then matches no daemons row, so only a writer that deletes the
-- choice itself can forget it. This release is that writer
-- (store.SetDefaultDaemon), and it ships first, so that when remembering
-- begins, every release still serving can clear a remembered default. Until
-- then this table is empty and nothing reads it.
CREATE TABLE user_default_daemons (
    user_id text PRIMARY KEY,
    daemon_id text NOT NULL,
    chosen_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE user_default_daemons IS
    'The machine each user made their default, remembered by machine id with no foreign key so it outlives the registry deleting that machine''s daemons row.';
