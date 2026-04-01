-- +goose Up
-- Migrate remaining TEXT enum columns to INTEGER for daemons and tool_execution_requests.

-- 1. daemons.status → DaemonStatus (active=1, idle=2, disconnected=3)
ALTER TABLE daemons DROP CONSTRAINT IF EXISTS daemons_status_check;
ALTER TABLE daemons ALTER COLUMN status DROP DEFAULT;
ALTER TABLE daemons ALTER COLUMN status TYPE INTEGER USING
    CASE status
        WHEN 'active' THEN 1
        WHEN 'idle' THEN 2
        WHEN 'disconnected' THEN 3
        ELSE 0
    END;
ALTER TABLE daemons ALTER COLUMN status SET DEFAULT 3;

-- 2. tool_execution_requests.status → ToolExecutionStatus
--    pending=1, executing=2, completed=3, failed=4, cancelled=5, timeout=6
DROP INDEX IF EXISTS idx_tool_exec_cleanup;
ALTER TABLE tool_execution_requests DROP CONSTRAINT IF EXISTS tool_execution_requests_status_check;
ALTER TABLE tool_execution_requests ALTER COLUMN status DROP DEFAULT;
ALTER TABLE tool_execution_requests ALTER COLUMN status TYPE INTEGER USING
    CASE status
        WHEN 'pending' THEN 1
        WHEN 'executing' THEN 2
        WHEN 'completed' THEN 3
        WHEN 'failed' THEN 4
        WHEN 'cancelled' THEN 5
        WHEN 'timeout' THEN 6
        ELSE 0
    END;
ALTER TABLE tool_execution_requests ALTER COLUMN status SET DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_tool_exec_cleanup ON tool_execution_requests(status, completed_at)
    WHERE status IN (3, 4, 5, 6);

-- +goose Down
-- Pre-launch migration; down is not supported.
