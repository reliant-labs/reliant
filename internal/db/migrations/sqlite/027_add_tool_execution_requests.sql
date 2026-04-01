-- +goose Up
-- Tool Execution Requests: Queue for remote tool execution via tools-daemon
-- Survives daemon disconnects, enables async execution, provides full audit trail

CREATE TABLE tool_execution_requests (
    id TEXT PRIMARY KEY,
    
    -- Ownership & Context
    user_id TEXT NOT NULL,         -- Which user's daemon should execute this
    chat_id TEXT NOT NULL,         -- Which chat this belongs to
    project_id TEXT,               -- Optional: which project context
    
    -- Request Data (what to execute)
    tool_name TEXT NOT NULL,
    tool_input TEXT NOT NULL,      -- JSON-encoded tool parameters
    tool_call_id TEXT NOT NULL,    -- LLM tool call ID (for matching results)
    content_block_id TEXT NOT NULL, -- DB content block ID
    
    -- Execution Context (serialized rctx.ToolContext)
    context_json TEXT,             -- Full context for tool execution
    working_dir TEXT,              -- Override working directory
    timeout_ms INTEGER,            -- Execution timeout in milliseconds
    
    -- Execution State
    status TEXT NOT NULL CHECK (status IN (
        'pending',     -- Queued, waiting for daemon
        'executing',   -- Daemon is processing
        'completed',   -- Successfully executed
        'failed',      -- Execution failed
        'cancelled',   -- Cancelled by user/system
        'timeout'      -- Timed out waiting for daemon
    )),
    
    daemon_id TEXT,                -- Which daemon picked this up
    started_at DATETIME,           -- When execution started
    completed_at DATETIME,         -- When execution finished
    
    -- Result Data (written by daemon)
    success BOOLEAN,               -- Whether tool succeeded
    is_error BOOLEAN,              -- Whether result is an error
    content TEXT,                  -- Tool output/result
    metadata TEXT,                 -- JSON-encoded metadata
    error_message TEXT,            -- Error message if failed
    error_code TEXT,               -- Error code for categorization
    
    -- Timestamps
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- Foreign Keys
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

-- Indices for efficient querying
CREATE INDEX idx_tool_exec_user_status ON tool_execution_requests(user_id, status);
CREATE INDEX idx_tool_exec_chat ON tool_execution_requests(chat_id, created_at DESC);
CREATE INDEX idx_tool_exec_status_created ON tool_execution_requests(status, created_at);
CREATE INDEX idx_tool_exec_tool_call ON tool_execution_requests(tool_call_id);
CREATE INDEX idx_tool_exec_daemon ON tool_execution_requests(daemon_id, status)
    WHERE daemon_id IS NOT NULL;

-- Index for cleanup queries (find old completed/failed requests)
CREATE INDEX idx_tool_exec_cleanup ON tool_execution_requests(status, completed_at)
    WHERE status IN ('completed', 'failed', 'cancelled', 'timeout');

-- +goose Down
DROP TABLE IF EXISTS tool_execution_requests;
