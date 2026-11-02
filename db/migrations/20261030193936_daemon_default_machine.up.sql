-- forge:backward-compatible — one added column the previous release never reads or writes: its explicit-column daemon inserts take the false default, and no machine is anyone's default until this release sets one.

-- The machine a user chose as their default: where a chat, a worktree or
-- any other work that names no machine runs (toolexec default resolution,
-- daemonplacement.ProjectDefaultDaemonID). At most one per user. It lives on
-- the machine's own row so removing the machine (DeleteDaemonRow) clears it,
-- and default resolution falls back to its machine-state rule.
ALTER TABLE daemons
    ADD COLUMN is_default boolean DEFAULT false NOT NULL;

CREATE UNIQUE INDEX daemons_one_default_per_user ON daemons (user_id) WHERE is_default;
