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
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// LocalToolExecutor handles the shared logic for executing tools locally (in-process or via daemon)
type LocalToolExecutor struct {
	toolsFactory *tools.ToolsFactory
	daemon       daemon.Client
	mcpBinder    MCPContextBinder
	egress       EgressLimiter
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

// DaemonEgressSubject is the one subject every egress call through a
// LocalToolExecutor is keyed on. A daemon serves one user, so its rate and
// in-flight budgets are the daemon's own; there is no second account to tell
// apart from the first.
const DaemonEgressSubject = "daemon"

// SetEgressLimiter bounds the egress tools this executor runs (fetch,
// websearch, sourcegraph, integration actions): the daemon's cap on what one
// machine pushes at a third party, and out of the hub's shared NAT when it is a
// managed daemon. Nil (the default) admits everything.
//
// An abuse brake for a well-behaved daemon, not a security boundary: the
// daemon is user-controlled and can run without it. The server-side NAT
// protections are the backstop.
func (e *LocalToolExecutor) SetEgressLimiter(l EgressLimiter) { e.egress = l }

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

	// The project and its checkout on THIS machine: the request's project
	// path is the dispatcher's projectcheckout resolution for the machine the
	// call was pinned to, never projects.path.
	var project *rctx.Project
	if projectMap, ok := contextMap["project"].(map[string]interface{}); ok {
		projectID, _ := projectMap["id"].(string)
		projectPath, _ := projectMap["path"].(string)
		projectName, _ := projectMap["name"].(string)

		project = &rctx.Project{
			ID:       projectID,
			Name:     projectName,
			Checkout: projectPath,
		}
	}

	// Extract worktree info if present
	var worktreeInfo *rctx.WorktreeInfo
	if worktreeMap, ok := contextMap["worktree"].(map[string]interface{}); ok {
		worktreeID, _ := worktreeMap["id"].(string)
		worktreePath, _ := worktreeMap["path"].(string)
		worktreeDaemonID, _ := worktreeMap["daemon_id"].(string)
		if worktreePath != "" {
			worktreeInfo = &rctx.WorktreeInfo{
				ID:       worktreeID,
				Path:     worktreePath,
				DaemonID: worktreeDaemonID,
			}
		}
	}

	// Create tool context with worktree info
	toolContext := rctx.NewToolContext(ctx, chatID, thread, project, worktreeInfo)
	// A workflow run step, marked by the server (ToolRequest.RunStep). Read
	// from the context map only — never from toolInput, which a model writes.
	toolContext.RunStep, _ = contextMap[RunStepContextKey].(bool)
	if toolContext.RunStep {
		toolContext.RunWorkDir, _ = contextMap[RunWorkDirContextKey].(string)
	}
	toolContext.BoundParams = boundParamsFrom(contextMap)
	if machine, ok := contextMap[RunMachineContextKey].(map[string]interface{}); ok {
		toolContext.RunMachine.ID, _ = machine["id"].(string)
		toolContext.RunMachine.Name, _ = machine["name"].(string)
	}

	// Decode the project's nested repos so tools with a `repo` param can
	// resolve it to a workspace-relative path. Absent when single-repo.
	if reposRaw, ok := contextMap["repos"].([]interface{}); ok {
		repos := make([]*core.Repo, 0, len(reposRaw))
		for _, item := range reposRaw {
			obj, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			id, _ := obj["id"].(string)
			name, _ := obj["name"].(string)
			rel, _ := obj["relative_path"].(string)
			repos = append(repos, &core.Repo{ID: id, Name: name, RelativePath: rel})
		}
		toolContext.Repos = repos
	}
	if daemonClient != nil {
		// An org that forces `credentials: none` on its runs' commands
		// (the worker's OrgStamp; RemoteExecutor.applyMachinePolicy) gets it
		// on every command this tool builds, here on the worker and on a
		// machine that reads the stamp: the tool reaches a command only
		// through this client.
		if daemonpolicy.OrgStampFrom(contextMap).CredentialsForced() {
			daemonClient = daemon.ForceCredentialsNone(daemonClient)
		}
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

	// The executor's factory was created at startup without the project's
	// skills, so the skill tool gets them from the context: execute_tools
	// reads them from the project's config row when its batch calls `skill`.
	// They used to come from a process-global store that call_llm filled,
	// which a worker restart — or a second worker — emptied.
	if toolsFactory != nil {
		if skills := tools.SkillsFrom(ctx); len(skills) > 0 {
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

	// Admitted only once the tool is known to exist, so a daemon without it
	// still answers TOOL_NOT_FOUND (which is how prefer-daemon falls back).
	releaseEgress, refusal := e.admitEgress(ctx, toolName, toolInput)
	if refusal != nil {
		return refusal
	}
	defer releaseEgress()

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

// boundParamsFrom is the parameters the run's configuration bound, as the
// server recorded them in the context map (ToolRequest.BoundParams). The map
// has crossed JSON on the way to a daemon, and stayed a []string on the way to
// a worker.
func boundParamsFrom(contextMap map[string]interface{}) []string {
	var out []string
	switch bound := contextMap[BoundParamsContextKey].(type) {
	case []string:
		out = append(out, bound...)
	case []interface{}:
		for _, name := range bound {
			if s, ok := name.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
