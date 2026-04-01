-- +goose Up
-- Migrate remaining TEXT enum columns to INTEGER for daemons and tool_execution_requests.
-- Pre-launch migration; existing data is converted.

-- ============================================================================
-- 1. daemons.status: DaemonStatus
--    active=1, idle=2, disconnected=3
-- ============================================================================
DROP INDEX IF EXISTS idx_daemons_status;
ALTER TABLE daemons ADD COLUMN status_new INTEGER NOT NULL DEFAULT 3;
UPDATE daemons SET status_new = CASE status
    WHEN 'active' THEN 1
    WHEN 'idle' THEN 2
    WHEN 'disconnected' THEN 3
    ELSE 0
END;
ALTER TABLE daemons DROP COLUMN status;
ALTER TABLE daemons RENAME COLUMN status_new TO status;
CREATE INDEX idx_daemons_status ON daemons(status);

-- ============================================================================
-- 2. tool_execution_requests.status: ToolExecutionStatus
--    pending=1, executing=2, completed=3, failed=4, cancelled=5, timeout=6
-- ============================================================================
DROP INDEX IF EXISTS idx_tool_exec_user_status;
DROP INDEX IF EXISTS idx_tool_exec_status_created;
DROP INDEX IF EXISTS idx_tool_exec_daemon;
DROP INDEX IF EXISTS idx_tool_exec_cleanup;
ALTER TABLE tool_execution_requests ADD COLUMN status_new INTEGER NOT NULL DEFAULT 1;
UPDATE tool_execution_requests SET status_new = CASE status
    WHEN 'pending' THEN 1
    WHEN 'executing' THEN 2
    WHEN 'completed' THEN 3
    WHEN 'failed' THEN 4
    WHEN 'cancelled' THEN 5
    WHEN 'timeout' THEN 6
    ELSE 0
END;
ALTER TABLE tool_execution_requests DROP COLUMN status;
ALTER TABLE tool_execution_requests RENAME COLUMN status_new TO status;
CREATE INDEX idx_tool_exec_user_status ON tool_execution_requests(user_id, status);
CREATE INDEX idx_tool_exec_status_created ON tool_execution_requests(status, created_at);
CREATE INDEX idx_tool_exec_daemon ON tool_execution_requests(daemon_id, status)
    WHERE daemon_id IS NOT NULL;
CREATE INDEX idx_tool_exec_cleanup ON tool_execution_requests(status, completed_at)
    WHERE status IN (3, 4, 5, 6);

-- +goose Down
-- Pre-launch migration; down is not supported.
