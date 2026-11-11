-- forge:backward-compatible — adds two triggers on daemons and resyncs user_default_daemons from daemons.is_default. The previous release (the one that added user_default_daemons) reads and writes only is_default, which the triggers keep in step, and its clear deletes the remembered default itself, so it stays correct beside this one and after a rollback. Do not ship this in the same release as 20261109190012_user_default_daemons: the release before that one cannot forget a remembered default.

-- Migration: restore_default_daemon
--
-- The contract half of making a user's default machine survive the registry
-- recreating its row (20261109190012_user_default_daemons is the expand half).
--
-- The default lives on the machine's daemons row (is_default), and the
-- registry deletes that row when the control plane's set omits the machine
-- and inserts it again, with is_default false, when the machine reconnects: a
-- laptop's default was lost across sleeps and app updates.
--
-- is_default stays the one column every release reads and writes. Two
-- triggers keep it durable whichever release wrote:
--
--   - daemons_record_default: a change of is_default is remembered in
--     user_default_daemons. Set on a machine, that machine is the user's
--     default; cleared on the remembered machine, the user has none.
--   - daemons_restore_default: a row inserted for the remembered machine comes
--     back with is_default set, unless another of the user's machines is the
--     default (daemons_one_default_per_user allows only one).
--
-- A clear while the remembered machine has no row matches no daemons row, so
-- no trigger sees it; store.SetDefaultDaemon deletes the remembered default
-- itself, in this release and the previous one.
--
-- Resync first: whatever the table holds is replaced by the current
-- is_default rows, so nothing written before every serving release could
-- clear it is ever restored.
DELETE FROM user_default_daemons;

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
