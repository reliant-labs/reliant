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
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
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
	// Credentials is the node's `credentials` setting: "none" runs the command
	// with no GitHub, git, ssh or cloud credentials.
	Credentials string `json:"credentials,omitempty"`
	// SaveMessage delegates the run node's save_message to the ActivityWrapper
	// (see types.SaveMessageRequest). The handler never reads it.
	SaveMessage *activitytypes.SaveMessageRequest `json:"save_message,omitempty" reliant:"-"`
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
// This activity executes shell commands via the daemon's shell tool.
type ExecuteRunStepActivity struct {
	repo         ExecuteRunStepStore
	toolExecutor toolexec.ToolExecutor
	runExecutor  RunExecutor // optional override for testing
	// undeliveredRetryDelays overrides defaultUndeliveredRetryDelays; tests
	// shrink it.
	undeliveredRetryDelays []time.Duration
}

// NewExecuteRunStepActivity creates a new ExecuteRunStepActivity.
// toolExecutor routes commands through the daemon. runExecutor is an optional
// testing override; if non-nil it is used instead of toolExecutor.
func NewExecuteRunStepActivity(repo ExecuteRunStepStore, toolExecutor toolexec.ToolExecutor, runExecutor RunExecutor) *ExecuteRunStepActivity {
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

	// IDEMPOTENCY: a shell command runs at most once per step dispatch.
	//
	// A command is sent again only on proof it never ran, and that proof is
	// acted on inside the attempt that has it (executeUntilDelivered); a
	// failure that proves nothing — a timeout, a dropped connection — fails the
	// step without a retry (runStepTransportFailure). So a Temporal retry
	// reaches this point only after an attempt that ended with no verdict at
	// all (a worker that died, a heartbeat timeout) and may have started the
	// command: it is refused, once, and not retried again. The workflow
	// pauses the step (RetryExhausted) and a resume dispatches it afresh.
	attemptNumber := activity.GetInfo(ctx).Attempt
	if attemptNumber > 1 {
		msg := "Error: the previous attempt at this command ended without a result and may have started it; it was not run again"
		return ExecuteRunStepOutput{
			Stderr:   msg,
			Output:   msg,
			ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds(),
		}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("activity retry detected (attempt %d): the previous attempt may have started the shell command, refusing to re-execute it", attemptNumber),
			runStepRetryRefusedErrType, nil)
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

	// A shell command cannot run without a machine. Launch refuses a
	// no-machine run of a workflow with a `run` node, so this is a backstop:
	// a failed COMMAND (exit -1), not a Go error, so the step neither burns
	// Temporal retries nor enters the retry-exhaustion pause waiting for a
	// machine that is not coming.
	if execCtx.NoMachine {
		refusal := nomachine.Refusal(model.NodeTypeRun)
		return ExecuteRunStepOutput{Stderr: refusal, Output: refusal, ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds()}, nil
	}

	// Propagate daemon selector from workflow input. Priority: explicit
	// node/workflow-level selector > durable chat pin > worktree owner
	// (set by resolveRunExecutorContext) > default resolution. Override the
	// resolved binding only when the input actually carries a selector.
	if input.DaemonSelector != nil {
		execCtx.DaemonSelector = input.DaemonSelector
	}
	credentials, err := daemonpolicy.ParseCredentials(input.Credentials)
	if err != nil {
		// A typo must not run the command with credentials the author meant to
		// withhold: fail the step as a command verdict, with no retry.
		msg := "run node: " + err.Error()
		return ExecuteRunStepOutput{Stderr: msg, Output: msg, ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds()}, nil
	}
	execCtx.Credentials = credentials

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
	retryDelays := a.undeliveredRetryDelays
	if retryDelays == nil {
		retryDelays = defaultUndeliveredRetryDelays
	}
	stdout, stderr, exitCode, interrupted, err := executeUntilDelivered(ctx, executor, retryDelays, input.Command, workingDir, timeout)
	if err != nil {
		// A transport failure is not a command verdict: fail the activity
		// rather than return a synthetic exit code the workflow would read
		// as one. Not retryable either way — see runStepTransportFailure.
		var transportErr *toolexec.TransportError
		if errors.As(err, &transportErr) {
			activity.GetLogger(ctx).Warn("[RunStep] no result came back from the machine",
				"chat_id", input.ChatID,
				"step_id", input.StepID,
				"code", transportErr.Code,
				"never_ran", transportErr.NotDispatched,
				"detail", transportErr.Detail,
			)
			return ExecuteRunStepOutput{}, runStepTransportFailure(transportErr)
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
		logger.Debug("[RunStep] Run output written to chat_updates",
			"chat_id", input.ChatID,
			"step_id", input.StepID,
			"exit_code", output.ExitCode,
			"duration_ms", output.Duration)
	}
}

// Temporal error types of the run step's non-retryable failures.
const (
	// runStepRetryRefusedErrType: a retry after an attempt that may have
	// started the command.
	runStepRetryRefusedErrType = "RunStepRetryRefused"
	// runStepNotDispatchedErrType: the machine could not be reached; the
	// command never ran.
	runStepNotDispatchedErrType = "RunStepNotDispatched"
	// runStepOutcomeUnknownErrType: the command was sent and no result came
	// back; it may have run.
	runStepOutcomeUnknownErrType = "RunStepOutcomeUnknown"
)

// defaultUndeliveredRetryDelays are the waits before each re-send of a command that
// provably never reached a machine, within one attempt. The same ladder as a
// step's Temporal retries (1s doubling, five sends in all), held here because
// only this attempt knows the command never ran: a Temporal retry starts with
// no record of how the attempt before it ended, so it could not tell this
// failure from one where the command started. The router has already ridden
// out a short disconnect inside each send (DaemonReconnectGrace).
var defaultUndeliveredRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// executeUntilDelivered runs command through executor, sending it again after
// each of delays while a failure proves it never reached a machine
// (TransportError.NotDispatched).
// Every other outcome — a verdict, a failure that may have followed a start —
// is returned as it came: it is sent once.
func executeUntilDelivered(ctx context.Context, executor RunExecutor, delays []time.Duration, command, workingDir string, timeoutMs int) (stdout, stderr string, exitCode int, interrupted bool, err error) {
	for i := 0; ; i++ {
		stdout, stderr, exitCode, interrupted, err = executor.ExecuteCommand(ctx, command, workingDir, timeoutMs, nil)
		var transportErr *toolexec.TransportError
		if err == nil || !errors.As(err, &transportErr) || !transportErr.NotDispatched || i >= len(delays) {
			return stdout, stderr, exitCode, interrupted, err
		}
		activity.GetLogger(ctx).Info("[RunStep] command never reached a machine; sending it again",
			"code", transportErr.Code, "retry_in", delays[i], "detail", transportErr.Detail)
		timer := time.NewTimer(delays[i])
		select {
		case <-ctx.Done():
			timer.Stop()
			return stdout, stderr, exitCode, interrupted, err
		case <-timer.C:
		}
	}
}

// runStepTransportFailure is the activity error for a command whose result
// never came back. Neither kind is retryable: one that never ran was already
// sent again by executeUntilDelivered, and one that may have run must not be.
// The workflow handles it as it handled the old refused retries, only without
// four of them first: it pauses the step (RetryExhausted), and a resume
// dispatches it afresh.
func runStepTransportFailure(transportErr *toolexec.TransportError) error {
	errType := runStepOutcomeUnknownErrType
	if transportErr.NotDispatched {
		errType = runStepNotDispatchedErrType
	}
	return temporal.NewNonRetryableApplicationError("failed to execute command: "+transportErr.Error(), errType, transportErr)
}

// shellQuote wraps a string in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
