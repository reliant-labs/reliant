// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type toolResponseType string

const (
	ToolResponseTypeText  toolResponseType = "text"
	ToolResponseTypeImage toolResponseType = "image"
)

type ToolResponse struct {
	Type         toolResponseType `json:"type"`
	Content      string           `json:"content"`
	AgentMessage string           `json:"agent_message,omitempty"` // Optional agent message to insert before tool result
	Metadata     string           `json:"metadata,omitempty"`
	IsError      bool             `json:"is_error"`
	Backgrounded bool             `json:"backgrounded,omitempty"` // True if command was converted to background
}

func NewTextResponse(content string) ToolResponse {
	return ToolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
	}
}

func WithResponseMetadata(response ToolResponse, metadata any) ToolResponse {
	if metadata != nil {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return response
		}
		response.Metadata = string(metadataBytes)
	}
	return response
}

func NewTextErrorResponse(content string) ToolResponse {
	return ToolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
		IsError: true,
	}
}

// ToolCall is an alias for the canonical message.ToolCall type.
// This provides a consistent type across the codebase while
// leveraging the canonical definition in the message package.
type ToolCall = message.ToolCall

type Tool interface {
	Name() string
	Description() string
	ParamSchema() *jsonschema.Schema
	RequiresPermission(rctx *rctx.ToolContext, params ToolCall) (bool, error)
	Run(rctx *rctx.ToolContext, params ToolCall) (ToolResponse, error)
}

// ReadOnlyTool is an optional interface that tools can implement to declare
// whether they are read-only (don't modify code, files, or external state)
type ReadOnlyTool interface {
	Tool
	IsReadOnly() bool
}

// func GetContextValues(rctx *rctx.Context) (string, string) {
// 	if rctx.Session != nil {
// 		return rctx.Session.ID, rctx.MessageID
// 	}
// 	return "", ""
// }

// GetWorkingDirectory retrieves the working directory from context
// Returns empty string if no working directory is set (NO FALLBACK TO OS.GETWD!)
func GetWorkingDirectory(rctx *rctx.ToolContext) (string, error) {
	if rctx == nil {
		return "", fmt.Errorf("nil context provided to GetWorkingDirectory")
	}
	dir := rctx.WorkingDir()
	if dir == "" {
		return "", fmt.Errorf("no working directory set in context")
	}
	return dir, nil
}

// GetWorktreeID retrieves the worktree ID from context
func GetWorktreeID(rctx *rctx.ToolContext) string {
	if rctx.Worktree != nil {
		return rctx.Worktree.ID
	}
	return ""
}

// GetChatID retrieves the chat ID from context
func GetChatID(rctx *rctx.ToolContext) string {
	return rctx.ChatID
}

// IsToolReadOnly determines if a tool is read-only (doesn't modify code, files, or state)
// It checks:
// 1. If the tool implements ReadOnlyTool interface
// 2. Pattern matching on tool name for common modifying prefixes/verbs
// 3. Whitelist of planning/organizational tools that should be allowed
func IsToolReadOnly(tool Tool) bool {
	// Check if tool explicitly declares itself as read-only
	if readOnlyTool, ok := tool.(ReadOnlyTool); ok {
		return readOnlyTool.IsReadOnly()
	}

	name := tool.Name()

	// Planning and task management tools are allowed in planning mode
	// These modify organizational state (plans/tasks) but not code
	planningTools := map[string]bool{
		"create_plan":       true,
		"update_plan":       true,
		"get_plan":          true,
		"add_task":          true,
		"update_task":       true,
		"create_subtask":    true,
		"list_tasks":        true,
		"add_dependency":    true,
		"remove_dependency": true,
		"list_ready_tasks":  true,
	}
	if planningTools[name] {
		return true // Planning tools are considered read-only
	}

	// Explicitly modifying tools that should be filtered
	// These are tools that write files or modify external state
	explicitlyModifying := map[string]bool{
		"metadata_writer": true, // Writes .reliant/project-meta.yaml
		"bash":            true, // Can execute arbitrary commands (Unix)
		"powershell":      true, // Can execute arbitrary commands (Windows)
		"bash_kill":       true, // Kills processes
		"worktree":        true, // Creates/modifies git worktrees
		"find_replace":    true, // Modifies files
		"move_code":       true, // Moves/copies code between files
	}
	if explicitlyModifying[name] {
		return false
	}

	// Pattern-based classification for tools that don't fit above categories
	// Check for common modifying verb prefixes
	modifyingPrefixes := []string{
		"write", "edit", "create", "delete", "update",
		"insert", "replace", "rename", "remove", "deploy",
		"install", "uninstall",
		"apply", "execute", "merge", "rebase", "reset",
		"pause", "restore", "kill", "stop", "start",
	}

	for _, prefix := range modifyingPrefixes {
		if strings.HasPrefix(name, prefix) ||
			strings.Contains(name, "_"+prefix+"_") ||
			strings.HasSuffix(name, "_"+prefix) {
			return false // Tool likely modifies state
		}
	}

	// Default to read-only for safety in planning mode
	// Tools should explicitly opt-in to being modifying
	return true
}
