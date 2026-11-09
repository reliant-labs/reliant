-- forge:backward-compatible — adds a table and two triggers on daemons. daemons.is_default stays the one column every release reads and writes; the triggers only record its changes and restore it on a recreated row, so the previous release reads and writes exactly what this one does, during a rolling deploy and after a rollback.

-- Migration: user_default_daemons
--
-- The machine a user chose as their default lives on that machine's daemons
-- row (is_default). But the row is a mirror of the control plane's daemon set,
-- not the user's own data: the registry snapshot deletes a row the control
-- plane's set omits once the machine has been offline for a moment, and the
-- machine's next connect inserts it again, with is_default false. A laptop's
-- default was lost that way across sleeps and app updates, and its owner had
-- to set it again and again.
--
-- user_default_daemons records the choice per user, by machine id, with no
-- foreign key to daemons, so it outlives the row. is_default stays the column
-- every release reads and writes; two triggers keep the two in step whichever
-- release wrote:
--
--   - daemons_record_default: a change of is_default records it. Set on a
--     machine, that machine is the user's choice; cleared on the machine that
--     was the choice, the user has none.
--   - daemons_restore_default: a row inserted for the machine the user chose
--     comes back with is_default set, unless another of the user's machines is
--     the default (daemons_one_default_per_user allows only one).
--
-- A choice naming a machine that never comes back matches nothing, and
-- default resolution falls back to its machine-state rule, exactly as with no
-- choice at all.
CREATE TABLE user_default_daemons (
    user_id text PRIMARY KEY,
    daemon_id text NOT NULL,
    chosen_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE user_default_daemons IS
    'The machine each user made their default. Kept in step with daemons.is_default by the daemons_record_default trigger, and keyed by machine id with no foreign key so it outlives the registry deleting that machine''s row (daemons_restore_default restores is_default when the row returns).';

INSERT INTO user_default_daemons (user_id, daemon_id)
SELECT user_id, id FROM daemons WHERE is_default;

CREATE FUNCTION daemons_record_default() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.is_default THEN
        INSERT INTO user_default_daemons (user_id, daemon_id, chosen_at)
        VALUES (NEW.user_id, NEW.id, CURRENT_TIMESTAMP)
        ON CONFLICT (user_id) DO UPDATE SET daemon_id = excluded.daemon_id, chosen_at = excluded.chosen_at;
    ELSE
        DELETE FROM user_default_daemons WHERE user_id = NEW.user_id AND daemon_id = NEW.id;
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER daemons_record_default AFTER UPDATE OF is_default ON daemons
    FOR EACH ROW WHEN (OLD.is_default IS DISTINCT FROM NEW.is_default)
    EXECUTE FUNCTION daemons_record_default();

CREATE FUNCTION daemons_restore_default() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT NEW.is_default
       AND EXISTS (SELECT 1 FROM user_default_daemons WHERE user_id = NEW.user_id AND daemon_id = NEW.id)
       AND NOT EXISTS (SELECT 1 FROM daemons WHERE user_id = NEW.user_id AND is_default) THEN
        NEW.is_default := true;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER daemons_restore_default BEFORE INSERT ON daemons
    FOR EACH ROW EXECUTE FUNCTION daemons_restore_default();
