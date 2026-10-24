-- forge:backward-compatible — additive NOT NULL columns with defaults; the previous release neither reads nor writes them, and its explicit-column inserts take the defaults.

-- The build a daemon reported when it registered (DaemonRegister version /
-- commit / commit_time_unix / forge_version), kept on the reachability lease:
-- it describes the stream that is attached now and goes with it on disconnect.
-- '' / 0 mean "unknown build" (a daemon that predates the fields, or an
-- unstamped build), never an error.
ALTER TABLE daemon_attachment
    ADD COLUMN build_version text NOT NULL DEFAULT '',
    ADD COLUMN build_commit text NOT NULL DEFAULT '',
    ADD COLUMN build_commit_time_unix bigint NOT NULL DEFAULT 0,
    ADD COLUMN build_forge_version text NOT NULL DEFAULT '';
