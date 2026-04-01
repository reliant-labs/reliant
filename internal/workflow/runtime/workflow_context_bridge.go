// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

const (
	workflowContextKeyID           = "id"
	workflowContextKeyName         = "name"
	workflowContextKeyChatID       = "chat_id"
	workflowContextKeyInputs       = "inputs"
	workflowContextKeyMode         = "mode"
	workflowContextKeyAgentName    = "agent_name"
	workflowContextKeyPrompt       = "prompt"
	workflowContextKeySpawnedBy    = "spawned_by"
	workflowContextKeyPath         = "path"
	workflowContextKeyBranch       = "branch"
	workflowContextKeyRunID        = "run_id"
	workflowContextKeySessionID    = "session_id"
	workflowContextKeyWorktreePath = "worktree_path"
)

// workflowContextToTyped converts a map[string]interface{} workflow context to *model.WorkflowContext.
// This is the canonical bridge between legacy map-based workflow contexts and typed CEL APIs.
func workflowContextToTyped(m map[string]interface{}) *model.WorkflowContext {
	if m == nil {
		return &model.WorkflowContext{}
	}

	wc := &model.WorkflowContext{}
	if id, ok := m[workflowContextKeyID].(string); ok {
		wc.ID = id
	}
	if name, ok := m[workflowContextKeyName].(string); ok {
		wc.Name = name
	}
	if path, ok := m[workflowContextKeyPath].(string); ok {
		wc.Path = path
	}
	if branch, ok := m[workflowContextKeyBranch].(string); ok {
		wc.Branch = branch
	}
	if mode, ok := m[workflowContextKeyMode].(string); ok {
		wc.Mode = mode
	}
	if runID, ok := m[workflowContextKeyRunID].(string); ok {
		wc.RunID = runID
	}
	if sessionID, ok := m[workflowContextKeySessionID].(string); ok {
		wc.SessionID = sessionID
	}
	if worktreePath, ok := m[workflowContextKeyWorktreePath].(string); ok {
		wc.WorktreePath = worktreePath
	}
	return wc
}
