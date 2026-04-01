-- +goose Up
-- Migration: Add background_processes table for process persistence across restarts
-- This enables reconnection to running processes after server restart

CREATE TABLE background_processes (
    id TEXT PRIMARY KEY,
    
    -- Process identification
    pid INTEGER,                    -- OS process ID (for validation on restart)
    command TEXT NOT NULL,          -- Full command that was executed
    working_dir TEXT NOT NULL,      -- Directory where command runs
    
    -- Association (primarily worktree-based per requirements)
    worktree_id TEXT,               -- Worktree this process belongs to
    project_id TEXT,                -- Project for organizational purposes
    chat_id TEXT,                   -- Optional: if spawned by LLM
    user_id TEXT NOT NULL,          -- Owner of the process
    
    -- Execution state
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN (
        'running',
        'completed',
        'failed',
        'killed',
        'killed_externally',
        'stale'                     -- Process that didn't survive restart
    )),
    exit_code INTEGER,              -- Exit code when completed
    
    -- Timing
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at DATETIME,
    
    -- Process signature for validation (command hash + start time)
    -- Used to verify PID still belongs to our process after restart
    signature TEXT,
    
    -- Source info (package command vs LLM-triggered)
    source_type TEXT NOT NULL DEFAULT 'llm' CHECK (source_type IN (
        'llm',                      -- Spawned by LLM agent
        'package_command',          -- Spawned via package manager UI
        'manual'                    -- Spawned manually via API
    )),
    package_type TEXT,              -- If source_type is package_command: makefile, npm, taskfile
    command_name TEXT,              -- If source_type is package_command: target/script name
    
    -- Metadata
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    -- Foreign keys
    FOREIGN KEY (worktree_id) REFERENCES worktrees(id) ON DELETE SET NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE SET NULL,
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE SET NULL
);

-- Index for querying processes by worktree (primary use case)
CREATE INDEX idx_bg_processes_worktree ON background_processes(worktree_id, status);

-- Index for querying processes by user
CREATE INDEX idx_bg_processes_user ON background_processes(user_id, status);

-- Index for querying processes by chat (for LLM-spawned processes)
CREATE INDEX idx_bg_processes_chat ON background_processes(chat_id) 
    WHERE chat_id IS NOT NULL;

-- Index for finding running processes (for restart recovery)
CREATE INDEX idx_bg_processes_running ON background_processes(status, pid) 
    WHERE status = 'running';

-- Index for cleanup of old completed processes
CREATE INDEX idx_bg_processes_cleanup ON background_processes(status, ended_at) 
    WHERE status IN ('completed', 'failed', 'killed', 'killed_externally', 'stale');

-- +goose Down
DROP INDEX IF EXISTS idx_bg_processes_cleanup;
DROP INDEX IF EXISTS idx_bg_processes_running;
DROP INDEX IF EXISTS idx_bg_processes_chat;
DROP INDEX IF EXISTS idx_bg_processes_user;
DROP INDEX IF EXISTS idx_bg_processes_worktree;
DROP TABLE IF EXISTS background_processes;
