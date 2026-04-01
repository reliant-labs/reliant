-- +goose Up
-- Workflow scenarios for testing workflows with mocked events
-- Each scenario defines a sequence of events that simulate workflow execution
CREATE TABLE workflow_scenarios (
    id TEXT PRIMARY KEY,
    workflow_draft_id TEXT,                -- FK to workflow_drafts (NULL if testing raw YAML)
    user_id TEXT NOT NULL,                 -- Owner of the scenario
    name TEXT NOT NULL,                    -- Human-readable scenario name
    description TEXT,                      -- What this scenario tests
    events TEXT NOT NULL,                  -- JSON array of event objects
    expect TEXT,                           -- JSON object with expected outcome
    
    -- Cached results from last run
    last_run_at DATETIME,
    last_run_status TEXT CHECK (last_run_status IN ('passed', 'failed', 'error')),
    last_run_result TEXT,                  -- JSON object with execution details
    
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (workflow_draft_id) REFERENCES workflow_drafts(id) ON DELETE CASCADE
);

-- Index for listing scenarios by workflow
CREATE INDEX idx_workflow_scenarios_draft ON workflow_scenarios(workflow_draft_id);

-- Index for listing scenarios by user
CREATE INDEX idx_workflow_scenarios_user ON workflow_scenarios(user_id);

-- Trigger to update updated_at on changes
-- +goose StatementBegin
CREATE TRIGGER update_workflow_scenarios_timestamp 
AFTER UPDATE ON workflow_scenarios 
BEGIN 
    UPDATE workflow_scenarios 
    SET updated_at = datetime('now', 'utc') 
    WHERE id = NEW.id; 
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS update_workflow_scenarios_timestamp;
DROP INDEX IF EXISTS idx_workflow_scenarios_user;
DROP INDEX IF EXISTS idx_workflow_scenarios_draft;
DROP TABLE IF EXISTS workflow_scenarios;
