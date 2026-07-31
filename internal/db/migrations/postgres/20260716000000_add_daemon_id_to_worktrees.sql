-- +goose Up
-- Records the daemon that physically created (and owns on disk) a worktree.
-- A worktree's git checkouts live at ~/.reliant/worktrees/<id>/ on ONE
-- specific daemon; tool execution for a worktree-bound chat must route to
-- that daemon or the path does not exist. Nullable: pre-existing rows have no
-- recorded owner and fall back to default daemon resolution.
ALTER TABLE worktrees ADD COLUMN daemon_id TEXT;

-- +goose Down
ALTER TABLE worktrees DROP COLUMN daemon_id;
