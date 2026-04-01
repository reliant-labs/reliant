-- Drop unused description and actions columns from approvals table.
-- +goose Up
-- Drop unused description and actions columns from approvals table.
-- description: never rendered in the UI (dead code path)
-- actions: broken end-to-end (schema mismatch in gRPC layer), default approve/deny always used
ALTER TABLE approvals DROP COLUMN description;
ALTER TABLE approvals DROP COLUMN actions;

-- +goose Down
ALTER TABLE approvals ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN actions TEXT NOT NULL DEFAULT '[]';
