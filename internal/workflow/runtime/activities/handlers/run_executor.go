// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// RunExecutor defines the interface for executing shell commands.
// This abstraction allows for mocking in tests.
type RunExecutor interface {
	// ExecuteCommand runs a command and returns stdout, stderr, exit code, and whether it was interrupted.
	// It should execute the command in the specified working directory with the given timeout.
	ExecuteCommand(
		ctx context.Context,
		command string,
		workingDir string,
		timeoutMs int,
		env map[string]string,
	) (stdout, stderr string, exitCode int, interrupted bool, err error)
}

// RunExecutorContext provides the IDs needed to build a ToolRequest for remote execution.
// Set by the activity before calling ExecuteCommand.
type RunExecutorContext struct {
	UserID         string
	ChatID         string
	ProjectID      string
	ProjectPath    string
	ProjectName    string
	WorktreeID     string
	WorktreePath   string
	DaemonSelector *toolexec.DaemonSelector // Target daemon for execution (optional)
}

// RemoteRunExecutor routes shell commands through the daemon via ToolExecutor.
// This replaces direct exec.Command so commands execute on the user's machine.
type RemoteRunExecutor struct {
	executor toolexec.ToolExecutor
	execCtx  RunExecutorContext
}

// NewRemoteRunExecutor creates a RemoteRunExecutor that routes commands through the daemon.
func NewRemoteRunExecutor(executor toolexec.ToolExecutor) *RemoteRunExecutor {
	return &RemoteRunExecutor{
		executor: executor,
	}
}

// SetContext sets the execution context (IDs) for subsequent ExecuteCommand calls.
func (e *RemoteRunExecutor) SetContext(execCtx RunExecutorContext) {
	e.execCtx = execCtx
}

// bashInput is the JSON structure the bash tool expects.
type bashInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // timeout in milliseconds
}

// ExecuteCommand routes a shell command through the daemon's bash tool.
func (e *RemoteRunExecutor) ExecuteCommand(
	ctx context.Context,
	command string,
	workingDir string,
	timeoutMs int,
	env map[string]string,
) (stdout, stderr string, exitCode int, interrupted bool, err error) {
	// Build bash tool input
	input := bashInput{
		Command: command,
		Timeout: timeoutMs,
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return "", "", -1, false, fmt.Errorf("failed to marshal bash tool input: %w", err)
	}

	// Determine effective working directory
	effectiveWorkingDir := workingDir
	if effectiveWorkingDir == "" {
		if e.execCtx.WorktreePath != "" {
			effectiveWorkingDir = e.execCtx.WorktreePath
		} else {
			effectiveWorkingDir = e.execCtx.ProjectPath
		}
	}

	// Build tool request
	req := &toolexec.ToolRequest{
		ToolName:       "bash",
		ToolInput:      string(inputJSON),
		UserID:         e.execCtx.UserID,
		ChatID:         e.execCtx.ChatID,
		ProjectID:      e.execCtx.ProjectID,
		WorktreeID:     e.execCtx.WorktreeID,
		ProjectPath:    e.execCtx.ProjectPath,
		ProjectName:    e.execCtx.ProjectName,
		WorktreePath:   e.execCtx.WorktreePath,
		WorkingDir:     effectiveWorkingDir,
		Timeout:        time.Duration(timeoutMs) * time.Millisecond,
		Environment:    env,
		DaemonSelector: e.execCtx.DaemonSelector,
	}

	// Execute through the daemon
	result, err := e.executor.ExecuteTool(ctx, req)
	if err != nil {
		return "", "", -1, false, fmt.Errorf("tool execution failed: %w", err)
	}

	// Map tool result back to RunExecutor contract
	if result == nil {
		return "", "", -1, false, fmt.Errorf("nil result from tool executor")
	}

	if result.IsError {
		// Tool reported an error — treat as non-zero exit
		return "", result.Content, 1, false, nil
	}

	// The bash tool returns structured JSON: {"stdout": "...", "stderr": "...", "exit_code": 0}
	var bashOutput struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(result.Content), &bashOutput); err != nil {
		// Fallback: treat content as plain text stdout (e.g. legacy format)
		return result.Content, "", 0, false, nil
	}

	return bashOutput.Stdout, bashOutput.Stderr, bashOutput.ExitCode, false, nil
}

// resolveRunExecutorContext loads the IDs needed for remote execution from the DB.
func resolveRunExecutorContext(ctx context.Context, repo db.Repository, chatID string) (RunExecutorContext, error) {
	chat, err := repo.GetChat(ctx, chatID)
	if err != nil {
		return RunExecutorContext{}, fmt.Errorf("failed to get chat: %w", err)
	}

	if chat.ProjectID == "" {
		return RunExecutorContext{}, fmt.Errorf("chat %s has no project_id", chatID)
	}

	project, err := repo.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return RunExecutorContext{}, fmt.Errorf("failed to get project: %w", err)
	}
	if project == nil {
		return RunExecutorContext{}, fmt.Errorf("project not found for chat %s", chatID)
	}
	if project.Path == "" {
		return RunExecutorContext{}, fmt.Errorf("project %s has no path", project.ID)
	}

	execCtx := RunExecutorContext{
		UserID:      project.UserID,
		ChatID:      chatID,
		ProjectID:   project.ID,
		ProjectPath: project.Path,
		ProjectName: project.Name,
	}

	// Check if chat has a worktree
	if chat.WorktreeID != nil && *chat.WorktreeID != "" {
		worktree, err := repo.GetWorktree(ctx, *chat.WorktreeID)
		if err != nil {
			return RunExecutorContext{}, fmt.Errorf("worktree %s lookup failed: %w", *chat.WorktreeID, err)
		}
		execCtx.WorktreeID = worktree.ID
		execCtx.WorktreePath = worktree.Path
	}

	return execCtx, nil
}
