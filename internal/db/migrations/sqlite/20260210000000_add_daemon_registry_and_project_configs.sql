-- +goose Up
-- Daemon registry + project config snapshots for cloud-refactor foundation

CREATE TABLE IF NOT EXISTS daemons (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    hostname TEXT,
    platform TEXT,
    status TEXT NOT NULL DEFAULT 'disconnected' CHECK (status IN ('active', 'idle', 'disconnected')),
    capabilities TEXT,   -- JSON array
    project_paths TEXT,  -- JSON array of paths requiring config
    projects_json TEXT,  -- JSON array of discovered projects
    connected_at DATETIME,
    last_heartbeat DATETIME,
    disconnected_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_daemons_user_id ON daemons(user_id);
CREATE INDEX IF NOT EXISTS idx_daemons_status ON daemons(status);

CREATE TABLE IF NOT EXISTS project_configs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    daemon_id TEXT NOT NULL,
    user_config_yaml TEXT,
    project_config_yaml TEXT,
    local_config_yaml TEXT,
    mcp_configs TEXT, -- JSON object: scope -> mcp.json content
    pushed_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_project_configs_daemon_id ON project_configs(daemon_id);
CREATE INDEX IF NOT EXISTS idx_project_configs_pushed_at ON project_configs(pushed_at DESC);

-- +goose Down
DROP TABLE IF EXISTS project_configs;
DROP TABLE IF EXISTS daemons;
