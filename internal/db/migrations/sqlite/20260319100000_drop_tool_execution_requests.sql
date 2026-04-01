-- +goose Up
DROP TABLE IF EXISTS tool_execution_requests;

-- +goose Down
-- no-op: pre-launch, no rollback needed
