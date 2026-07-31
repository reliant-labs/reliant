// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// ExecuteRunStepInput is the input for ExecuteRunStep activity
type ExecuteRunStepInput struct {
	WorkflowID     string                   `json:"workflow_id"`
	ChatID         string                   `json:"chat_id" reliant:"-"`
	StepID         string                   `json:"step_id"`
	Command        string                   `json:"command"`
	Timeout        int                      `json:"timeout"`                   // in milliseconds, default 5 minutes
	LoopNodeID     string                   `json:"loop_node_id,omitempty"`    // Loop context: which loop node spawned this
	LoopIteration  int                      `json:"loop_iteration"`            // Loop context: iteration index (0-indexed)
	DaemonSelector *toolexec.DaemonSelector `json:"daemon_selector,omitempty"` // Target daemon for execution
	LogFile        string                   `json:"log_file,omitempty"`        // Redirect stdout+stderr to this file (daemon-side)
}

// ExecuteRunStepOutput is the output from ExecuteRunStep activity
type ExecuteRunStepOutput struct {
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	Output       string `json:"output"` // combined stdout + stderr
	ExitCode     int    `json:"exit_code"`
	Interrupted  bool   `json:"interrupted"`
	Duration     int64  `json:"duration"`           // in milliseconds
	WorkingDir   string `json:"working_dir"`        // the directory where command was executed
	WorktreeID   string `json:"worktree_id"`        // worktree ID if used
	WorktreePath string `json:"worktree_path"`      // worktree path if used
	LogFile      string `json:"log_file,omitempty"` // absolute path to log file if written
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// ExecuteRunStepActivity implements TypedActivity[ExecuteRunStepInput, ExecuteRunStepOutput]
// This activity executes shell commands via the daemon's bash tool.
type ExecuteRunStepActivity struct {
	repo         db.Repository
	toolExecutor toolexec.ToolExecutor
	runExecutor  RunExecutor // optional override for testing
}

// NewExecuteRunStepActivity creates a new ExecuteRunStepActivity.
// toolExecutor routes commands through the daemon. runExecutor is an optional
// testing override; if non-nil it is used instead of toolExecutor.
func NewExecuteRunStepActivity(repo db.Repository, toolExecutor toolexec.ToolExecutor, runExecutor RunExecutor) *ExecuteRunStepActivity {
	return &ExecuteRunStepActivity{
		repo:         repo,
		toolExecutor: toolExecutor,
		runExecutor:  runExecutor,
	}
}

// Name returns the activity name for registration
func (a *ExecuteRunStepActivity) Name() string {
	return "ExecuteRunStep"
}

// DisplayName returns human-readable name for UI
func (a *ExecuteRunStepActivity) DisplayName() string {
	return "Run Command"
}

// Description returns what the activity does
func (a *ExecuteRunStepActivity) Description() string {
	return "Execute a shell command in the project's working directory"
}

// Category returns the activity category for UI grouping
func (a *ExecuteRunStepActivity) Category() schema.ActivityCategory {
	return schema.CategoryRunStep
}

// Execute contains PURE BUSINESS LOGIC only
// Middleware automatically handles:
// - ✅ Logging (entry/exit)
// - ✅ Duration tracking
// - ✅ Error handling
// - ✅ Step execution tracking (UI-only)
func (a *ExecuteRunStepActivity) Execute(ctx context.Context, input ExecuteRunStepInput) (ExecuteRunStepOutput, error) {
	startTime := time.Now()

	// IDEMPOTENCY: Check if this is a retry (attempt > 1)
	// DO NOT re-execute shell commands on retry - return error result instead
	attemptNumber := activity.GetInfo(ctx).Attempt
	if attemptNumber > 1 {
		// This is a retry - the command was already attempted
		// Return an error result instead of re-executing
		return ExecuteRunStepOutput{
			Stdout:       "",
			Stderr:       "Error: Activity retry detected - shell command was not re-executed for safety",
			Output:       "Error: Activity retry detected - shell command was not re-executed for safety",
			ExitCode:     -1,
			Interrupted:  false,
			Duration:     time.Since(startTime).Milliseconds(),
			WorkingDir:   "",
			WorktreeID:   "",
			WorktreePath: "",
		}, fmt.Errorf("activity retry detected (attempt %d) - refusing to re-execute shell command for idempotency", attemptNumber)
	}

	// Set default timeout if not provided (5 minutes)
	timeout := input.Timeout
	if timeout == 0 {
		timeout = int(5 * time.Minute / time.Millisecond)
	}

	// Resolve execution context from chat -> project -> worktree
	execCtx, err := resolveRunExecutorContext(ctx, a.repo, input.ChatID)
	if err != nil {
		return ExecuteRunStepOutput{}, fmt.Errorf("failed to resolve working directory: %w", err)
	}

	// Propagate daemon selector from workflow input. Priority: explicit
	// node/workflow-level selector > the worktree's owning daemon (set by
	// resolveRunExecutorContext) > default resolution. Only override the
	// worktree pin when the input actually carries a selector.
	if input.DaemonSelector != nil {
		execCtx.DaemonSelector = input.DaemonSelector
	}

	// Determine working directory
	workingDir := execCtx.ProjectPath
	worktreeID := execCtx.WorktreeID
	worktreePath := execCtx.WorktreePath
	if worktreePath != "" {
		workingDir = worktreePath
	}

	// Pick executor: test override or remote via daemon
	executor := a.runExecutor
	if executor == nil {
		remote := NewRemoteRunExecutor(a.toolExecutor)
		remote.SetContext(execCtx)
		executor = remote
	}

	// Execute the command
	stdout, stderr, exitCode, interrupted, err := executor.ExecuteCommand(
		ctx,
		input.Command,
		workingDir,
		timeout,
		nil, // no custom env vars for now
	)
	if err != nil {
		// A transport failure means the command never ran. Fail the activity so
		// the activity retry policy owns it, instead of returning a synthetic
		// exit code that the workflow would read as a real command verdict.
		var transportErr *toolexec.TransportError
		if errors.As(err, &transportErr) {
			activity.GetLogger(ctx).Error("[RunStep] command never reached a daemon",
				"command", input.Command,
				"code", transportErr.Code,
				"detail", transportErr.Detail,
			)
		}
		return ExecuteRunStepOutput{}, fmt.Errorf("failed to execute command: %w", err)
	}

	// Combine stdout and stderr for output field
	var outputBuilder strings.Builder
	if stdout != "" {
		outputBuilder.WriteString(stdout)
	}
	if stderr != "" {
		if outputBuilder.Len() > 0 {
			outputBuilder.WriteString("\n")
		}
		outputBuilder.WriteString(stderr)
	}

	duration := time.Since(startTime).Milliseconds()
	combinedOutput := outputBuilder.String()

	// Write log file if configured (daemon-side via executor)
	var logFilePath string
	if input.LogFile != "" {
		logFilePath = input.LogFile
		if !filepath.IsAbs(logFilePath) {
			logFilePath = filepath.Join(workingDir, logFilePath)
		}
		logFilePath = filepath.Clean(logFilePath)

		// Write structured JSON log with separated stdout/stderr and metadata.
		// This lets agents parse exit code, working directory, and output streams.
		logData := map[string]interface{}{
			"exit_code":   exitCode,
			"working_dir": workingDir,
			"command":     input.Command,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"stdout":      stdout,
			"stderr":      stderr,
		}
		logJSON, jsonErr := json.Marshal(logData)
		if jsonErr != nil {
			// Fallback to combined plain text if JSON fails
			logJSON = []byte(combinedOutput)
		}

		writeCmd := fmt.Sprintf(
			"mkdir -p %s && cat > %s <<'__RELIANT_LOG_EOF__'\n%s\n__RELIANT_LOG_EOF__",
			shellQuote(filepath.Dir(logFilePath)),
			shellQuote(logFilePath),
			string(logJSON),
		)
		_, _, _, _, writeErr := executor.ExecuteCommand(ctx, writeCmd, workingDir, 30000, nil)
		if writeErr != nil {
			logger := activity.GetLogger(ctx)
			logger.Warn("[RunStep] Failed to write log file (non-fatal)",
				"log_file", logFilePath,
				"error", writeErr,
			)
			// Graceful degradation: don't fail the step
			logFilePath = ""
		}
	}

	// Build the output struct
	output := ExecuteRunStepOutput{
		Stdout:       stdout,
		Stderr:       stderr,
		Output:       combinedOutput,
		ExitCode:     exitCode,
		Interrupted:  interrupted,
		Duration:     duration,
		WorkingDir:   workingDir,
		WorktreeID:   worktreeID,
		WorktreePath: worktreePath,
		LogFile:      logFilePath,
	}

	// Write run_output to chat_updates for UI display
	a.writeRunOutput(ctx, input, output)

	return output, nil
}

// ============================================================================
// HELPERS
// ============================================================================

// writeRunOutput writes a run_output update to chat_updates for UI display
// This allows run step output to appear in the UI without polluting LLM context
func (a *ExecuteRunStepActivity) writeRunOutput(ctx context.Context, input ExecuteRunStepInput, output ExecuteRunStepOutput) {
	logger := activity.GetLogger(ctx)
	activityInfo := activity.GetInfo(ctx)

	// Generate a unique ID for this run output
	runOutputID := uuid.New().String()

	// Build the run_output update data
	updateData := map[string]interface{}{
		"update_type":        "run_output",
		"id":                 runOutputID,
		"step_id":            input.StepID,
		"command":            input.Command,
		"stdout":             output.Stdout,
		"stderr":             output.Stderr,
		"output":             output.Output,
		"exit_code":          output.ExitCode,
		"interrupted":        output.Interrupted,
		"duration":           output.Duration,
		"working_dir":        output.WorkingDir,
		"worktree_id":        output.WorktreeID,
		"worktree_path":      output.WorktreePath,
		"workflow_id":        input.WorkflowID,
		"unique_activity_id": activityInfo.ActivityID, // For deduplication with save_message
		"timestamp":          time.Now().UTC().Format(time.RFC3339),
	}
	if output.LogFile != "" {
		updateData["log_file"] = output.LogFile
	}

	// Marshal to JSON
	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		logger.Error("[RunStep] Failed to marshal run_output update",
			"error", err,
			"chat_id", input.ChatID,
			"step_id", input.StepID)
		return
	}

	// Write to chat_updates (best-effort, don't fail the activity if this fails)
	entityID := fmt.Sprintf("run-%s-%s", input.StepID, runOutputID)
	if err := a.repo.CreateChatUpdate(ctx, input.ChatID, db.UpdateTypeRunOutput, entityID, string(updateDataJSON)); err != nil {
		logger.Error("[RunStep] Failed to create run_output chat_update",
			"error", err,
			"chat_id", input.ChatID,
			"step_id", input.StepID)
	} else {
		logger.Info("[RunStep] Run output written to chat_updates",
			"chat_id", input.ChatID,
			"step_id", input.StepID,
			"exit_code", output.ExitCode,
			"duration_ms", output.Duration)
	}
}

// shellQuote wraps a string in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
