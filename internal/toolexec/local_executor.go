// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// LocalToolExecutor handles the shared logic for executing tools locally (in-process or via daemon)
type LocalToolExecutor struct {
	toolsFactory *tools.ToolsFactory
	daemon       daemon.Client
	mcpBinder    MCPContextBinder
}

// NewLocalToolExecutor creates a new local tool executor
func NewLocalToolExecutor(toolsFactory *tools.ToolsFactory) *LocalToolExecutor {
	return &LocalToolExecutor{
		toolsFactory: toolsFactory,
	}
}

// SetDaemonClient sets the daemon client for tool execution.
// When set, tools receive it via rctx.ToolContext.Daemon.
func (e *LocalToolExecutor) SetDaemonClient(d daemon.Client) {
	e.daemon = d
}

// SetMCPContextBinder sets the execution-time MCP binder.
func (e *LocalToolExecutor) SetMCPContextBinder(binder MCPContextBinder) {
	e.mcpBinder = binder
}

// ExecutionResult holds the result of local tool execution
type ExecutionResult struct {
	Success      bool
	IsError      bool
	Backgrounded bool // True if tool was converted to background execution
	Content      string
	Metadata     string
	ErrorMessage string
	ErrorCode    string
	BinaryParts  []message.BinaryContent // Binary content (images, PDFs)
}

// ExecuteToolWithDaemon executes a tool locally with an explicit daemon client,
// without mutating the executor's shared field. This is the thread-safe variant
// used by RemoteExecutor.executeOnServer where each request may target a
// different user's daemon.
func (e *LocalToolExecutor) ExecuteToolWithDaemon(
	ctx context.Context,
	toolName string,
	toolInput string,
	toolCallID string,
	timeoutMs int,
	contextMap map[string]interface{},
	daemonOverride daemon.Client,
) *ExecutionResult {
	return e.executeTool(ctx, toolName, toolInput, toolCallID, timeoutMs, contextMap, daemonOverride)
}

// ExecuteTool executes a tool locally with the given parameters
// This is the single source of truth for local tool execution logic
func (e *LocalToolExecutor) ExecuteTool(
	ctx context.Context,
	toolName string,
	toolInput string,
	toolCallID string,
	timeoutMs int,
	contextMap map[string]interface{},
) *ExecutionResult {
	return e.executeTool(ctx, toolName, toolInput, toolCallID, timeoutMs, contextMap, e.daemon)
}

// executeTool is the internal implementation shared by ExecuteTool and ExecuteToolWithDaemon.
func (e *LocalToolExecutor) executeTool(
	ctx context.Context,
	toolName string,
	toolInput string,
	toolCallID string,
	timeoutMs int,
	contextMap map[string]interface{},
	daemonClient daemon.Client,
) *ExecutionResult {
	// Add user ID to context for analytics tracking
	if userID, ok := contextMap["user_id"].(string); ok && userID != "" {
		ctx = context.WithValue(ctx, auth.UserIDContextKey, userID)
	}

	// Parse tool input
	var inputMap map[string]interface{}
	if err := json.Unmarshal([]byte(toolInput), &inputMap); err != nil {
		return &ExecutionResult{
			Success:      false,
			IsError:      true,
			Content:      fmt.Sprintf("Failed to parse tool input: %v", err),
			ErrorMessage: err.Error(),
			ErrorCode:    "PARSE_ERROR",
		}
	}

	var tool tools.Tool

	// Extract context fields
	chatID, _ := contextMap["chat_id"].(string)
	thread, _ := contextMap["thread"].(string)

	// Create minimal project info
	var project *db.Project
	if projectMap, ok := contextMap["project"].(map[string]interface{}); ok {
		projectID, _ := projectMap["id"].(string)
		projectPath, _ := projectMap["path"].(string)
		projectName, _ := projectMap["name"].(string)

		project = &db.Project{
			ID:   projectID,
			Path: projectPath,
			Name: projectName,
		}
	}

	// Extract worktree info if present
	var worktreeInfo *rctx.WorktreeInfo
	if worktreeMap, ok := contextMap["worktree"].(map[string]interface{}); ok {
		worktreeID, _ := worktreeMap["id"].(string)
		worktreePath, _ := worktreeMap["path"].(string)
		if worktreePath != "" {
			worktreeInfo = &rctx.WorktreeInfo{
				ID:   worktreeID,
				Path: worktreePath,
			}
		}
	}

	// Create tool context with worktree info
	toolContext := rctx.NewToolContext(ctx, chatID, thread, project, worktreeInfo)
	if daemonClient != nil {
		toolContext = toolContext.WithDaemon(daemonClient)
	}
	if e.mcpBinder != nil {
		toolContext = e.mcpBinder.Bind(toolContext)
	}

	workingDir := toolContext.WorkingDir()
	if toolContext.MCP != nil && workingDir != "" && strings.HasPrefix(strings.TrimSpace(toolName), "mcp__") {
		toolContext.MCP.EnsureProjectServersLoaded(toolContext.Context, workingDir)
	}

	toolsFactory := e.toolsFactory
	if toolsFactory != nil {
		toolsFactory = toolsFactory.WithMCPProjectPath(workingDir)
	}

	// Inject skills from the global store for this chat. call_llm stores
	// skills per-chat; the executor's factory was created at startup without
	// them, so we clone with skills here so the skill tool sees them.
	if toolsFactory != nil && chatID != "" {
		if skills := tools.GetLoadedToolsStore().GetSkills(chatID); len(skills) > 0 {
			toolsFactory = toolsFactory.WithSkills(skills)
		}
	}

	if toolsFactory == nil {
		return &ExecutionResult{
			Success:      false,
			IsError:      true,
			Content:      "Tool execution failed: tools factory is not configured",
			ErrorMessage: "tools factory not configured",
			ErrorCode:    "EXECUTION_ERROR",
		}
	}

	// Get tool from factory (project-scoped for MCP tools)
	tool = toolsFactory.GetToolByName(toolName, toolContext.MCP)
	if tool == nil {
		return &ExecutionResult{
			Success:      false,
			IsError:      true,
			Content:      fmt.Sprintf("Tool '%s' not found", toolName),
			ErrorMessage: fmt.Sprintf("tool not found: %s", toolName),
			ErrorCode:    "TOOL_NOT_FOUND",
		}
	}

	// Apply timeout
	if timeoutMs > 0 {
		timeout := time.Duration(timeoutMs) * time.Millisecond
		toolContext, _ = toolContext.WithTimeout(timeout)
	}

	// Create tool call
	toolCall := tools.ToolCall{
		ID:    toolCallID,
		Name:  toolName,
		Input: toolInput,
	}

	// Execute tool
	response, err := tool.Run(toolContext, toolCall)
	if err != nil {
		return &ExecutionResult{
			Success:      false,
			IsError:      true,
			Content:      fmt.Sprintf("Tool execution failed: %v", err),
			ErrorMessage: err.Error(),
			ErrorCode:    "EXECUTION_ERROR",
		}
	}

	return &ExecutionResult{
		Success:      true,
		IsError:      response.IsError,
		Backgrounded: response.Backgrounded,
		Content:      response.Content,
		Metadata:     response.Metadata,
		BinaryParts:  response.BinaryParts,
		ErrorMessage: "",
		ErrorCode:    "",
	}
}
