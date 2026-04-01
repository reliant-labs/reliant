-- +goose Up
-- Add is_hidden to workflow_drafts
ALTER TABLE workflow_drafts ADD COLUMN is_hidden BOOLEAN NOT NULL DEFAULT 0;
CREATE INDEX idx_workflow_drafts_is_hidden ON workflow_drafts(is_hidden);
