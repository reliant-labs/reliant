// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// ExecuteRunStepInput is the input for ExecuteRunStep activity
type ExecuteRunStepInput struct {
	WorkflowID    string `json:"workflow_id"`
	ChatID        string `json:"chat_id" reliant:"-"`
	StepID        string `json:"step_id"`
	Command       string `json:"command"`
	Timeout       int    `json:"timeout"`                // in milliseconds, default 5 minutes
	LoopNodeID    string `json:"loop_node_id,omitempty"` // Loop context: which loop node spawned this
	LoopIteration int    `json:"loop_iteration"`         // Loop context: iteration index (0-indexed)
	// NodePath is the node's fully-qualified dotted graph position
	// ("gate.run" for a run node in loop gate's body), and Telemetry the run
	// it works for. The handler never reads either: the ActivityWrapper reads
	// them off this typed input for the step's row (step_executions.node_path
	// and run_id). Without the fields, decoding the workflow's input map into
	// this struct dropped both, so every run node inside a loop or a child
	// workflow was recorded under its bare id ("run") and with no run id.
	NodePath       string                        `json:"node_path,omitempty" reliant:"-"`
	Telemetry      *activitytypes.RunTelemetry   `json:"telemetry,omitempty" reliant:"-"`
	DaemonSelector *activitytypes.DaemonSelector `json:"daemon_selector,omitempty"` // Target daemon for execution
	LogFile        string                        `json:"log_file,omitempty"`        // Redirect stdout+stderr to this file (daemon-side)
	// Credentials is the node's `credentials` setting: "none" runs the command
	// with no GitHub, git, ssh or cloud credentials.
	Credentials string `json:"credentials,omitempty"`
	// WritableCheckouts is the node's `writable_checkouts`: the shared
	// checkouts a credentials-none command may write (credsandbox.Wrap).
	WritableCheckouts []string `json:"writable_checkouts,omitempty"`
	// WorkDir is the node's `work_dir`, resolved: the directory the command
	// starts in. A relative one is relative to the run's checkout; empty is
	// the checkout itself.
	WorkDir string `json:"work_dir,omitempty"`
	// LegacyLLMGateway is a run node's removed `llm_gateway`, as a step
	// scheduled by a release that still had it carries it, or as the workflow
	// passes it for a node a run recorded then declared
	// (runtime/removed_definition_keys.go). Never set otherwise. Such a
	// command was to run only with a gateway step key, which is no longer
	// minted, so it is refused rather than run without one.
	LegacyLLMGateway json.RawMessage `json:"llm_gateway,omitempty"`
	// SaveMessage delegates the run node's save_message to the ActivityWrapper
	// (see types.SaveMessageRequest). The handler never reads it.
	SaveMessage *activitytypes.SaveMessageRequest `json:"save_message,omitempty" reliant:"-"`
	// Observe hands the run node's observe: block to the ActivityWrapper (see
	// types.ObserveRequest). The handler never reads it.
	Observe *activitytypes.ObserveRequest `json:"observe,omitempty" reliant:"-"`
}

// ExecuteRunStepOutput is the output from ExecuteRunStep activity
type ExecuteRunStepOutput struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Output      string `json:"output"` // combined stdout + stderr
	ExitCode    int    `json:"exit_code"`
	Interrupted bool   `json:"interrupted"`
	// Truncated: the machine cut stdout or stderr to its output cap, so
	// they are not the command's whole output.
	Truncated    bool   `json:"truncated"`
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
	rows         WorkspaceRows
	toolExecutor toolexec.ToolExecutor
	runExecutor  RunExecutor // optional override for testing
	// machines reports whether the run's machine is attached while a command
	// that never reached it waits (WithMachineObserver). nil: the wait cannot
	// see the machine and re-sends on its backoff alone.
	machines MachineObserver
	// machineWait overrides defaultMachineWait; tests shrink it or give it a
	// fake clock.
	machineWait *machineWait
	// router resolves a run node's machine named by name, type or labels to
	// the machine whose checkout the command runs in (WithDaemonRouter).
	router toolexec.DaemonRouter
}

// WithDaemonRouter lets a run node that names its machine by name, type or
// labels run in THAT machine's checkout (resolveRunCheckout).
func (a *ExecuteRunStepActivity) WithDaemonRouter(router toolexec.DaemonRouter) *ExecuteRunStepActivity {
	a.router = router
	return a
}

// MachineObserver reports the state of the machine a run's commands execute
// on; the run's daemon selector travels on ctx (toolexec.WithDaemonSelector).
// toolexec.RunMachine implements it, as it does for wait_for_machine.
type MachineObserver interface {
	MachineStatus(ctx context.Context, userID string) (tools.MachineStatus, error)
}

// WithMachineObserver lets a command that never reached its machine watch the
// machine's connection while it waits, instead of re-sending blind.
func (a *ExecuteRunStepActivity) WithMachineObserver(observer MachineObserver) *ExecuteRunStepActivity {
	a.machines = observer
	return a
}

// NewExecuteRunStepActivity creates a new ExecuteRunStepActivity.
// toolExecutor routes commands through the daemon. runExecutor is an optional
// testing override; if non-nil it is used instead of toolExecutor.
func NewExecuteRunStepActivity(repo ExecuteRunStepStore, rows WorkspaceRows, toolExecutor toolexec.ToolExecutor, runExecutor RunExecutor) *ExecuteRunStepActivity {
	return &ExecuteRunStepActivity{
		repo:         repo,
		rows:         rows,
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
	info := activity.GetInfo(ctx)
	return a.execute(ctx, input, info.Attempt, runStepRequestID(info))
}

// runStepRequestID is the transport id of a run step's command: the same on
// every attempt of one scheduled activity, and on no other. Temporal keeps
// the activity id across retries and never reuses it within a run, and the
// run id tells runs apart, so a resume (a new activity) gets a new id. A
// hash, so it fits the request-id alphabet whatever the ids contain.
func runStepRequestID(info activity.Info) string {
	sum := sha256.Sum256([]byte(info.WorkflowExecution.ID + "\x00" + info.WorkflowExecution.RunID + "\x00" + info.ActivityID))
	return "runstep-" + hex.EncodeToString(sum[:16])
}

// execute is Execute for attempt number attempt, whose command travels as
// requestID.
func (a *ExecuteRunStepActivity) execute(ctx context.Context, input ExecuteRunStepInput, attempt int32, requestID string) (ExecuteRunStepOutput, error) {
	startTime := time.Now()

	// IDEMPOTENCY: a shell command runs at most once per step dispatch.
	//
	// A command is sent again only on proof it never ran, and that proof is
	// acted on inside the attempt that has it (executeUntilDelivered); a
	// failure that proves nothing — a timeout, a dropped connection — fails the
	// step without a retry (runStepTransportFailure). So a Temporal retry
	// reaches this point only after an attempt that ended with no verdict at
	// all (a worker that died or was released by its drain, a heartbeat
	// timeout) and may have started the command.
	//
	// The retry never sends it. It reattaches, best effort: the command went
	// out under requestID, the same on every attempt, and the machine keeps
	// it — running, or finished with its result held — so the retry waits for
	// THAT command and returns its real output and exit status. When the
	// machine cannot vouch for it (it restarted, its result is gone, it is too
	// old to be asked, or this attempt cannot even resolve where it ran) the
	// retry is refused, once, and not retried again: the workflow pauses the
	// step (RetryExhausted) and a resume dispatches it afresh.
	retry := attempt > 1
	refuse := func() (ExecuteRunStepOutput, error) {
		msg := "Error: the previous attempt at this command ended without a result and may have started it; it was not run again"
		return ExecuteRunStepOutput{
			Stderr:   msg,
			Output:   msg,
			ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds(),
		}, temporal.NewApplicationErrorWithOptions(
			fmt.Sprintf("activity retry detected (attempt %d): the previous attempt may have started the shell command, refusing to re-execute it", attempt),
			runStepRetryRefusedErrType, temporal.ApplicationErrorOptions{
				NonRetryable: true,
				// Benign: refusing is the designed at-most-once outcome, not a
				// fault — the run pauses and says why, and the user resumes
				// it. What ended the previous attempt was reported there.
				// Unclassified, each refusal was an ERROR issue (prod
				// 48b45b13).
				Category: temporal.ApplicationErrorCategoryBenign,
			})
	}

	// A command declared with the removed llm_gateway never runs without its
	// step key. A retry only reattaches to what its first attempt sent, so it
	// carries on; that key, no longer revoked here, lapses at its expiry.
	if !retry && hasLegacyLLMGateway(input.LegacyLLMGateway) {
		logging.WarnContext(ctx, "[RunStep] refused a step declared with the removed llm_gateway; the command was not run",
			"chat_id", input.ChatID, "step_id", input.StepID)
		return ExecuteRunStepOutput{Stderr: legacyLLMGatewayRefusal, Output: legacyLLMGatewayRefusal, ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds()}, nil
	}

	// Set default timeout if not provided (5 minutes)
	timeout := input.Timeout
	if timeout == 0 {
		timeout = int(5 * time.Minute / time.Millisecond)
	}

	// Resolve execution context from chat -> project -> worktree
	execCtx, err := resolveRunExecutorContext(ctx, a.repo, a.rows, input.ChatID)
	if err != nil {
		// A retry resolves where its command ran the way the attempt that
		// sent it did. Anything that stops it here, or at any of the checks
		// below, may be a verdict the first attempt returned without running
		// the command, or may not: the retry cannot know, so it refuses.
		if retry {
			return refuse()
		}
		return ExecuteRunStepOutput{}, fmt.Errorf("failed to resolve working directory: %w", err)
	}

	// A shell command cannot run without a machine. Launch refuses a
	// no-machine run of a workflow with a `run` node, so this is a backstop:
	// a failed COMMAND (exit -1), not a Go error, so the step neither burns
	// Temporal retries nor enters the retry-exhaustion pause waiting for a
	// machine that is not coming.
	if execCtx.NoMachine {
		if retry {
			return refuse()
		}
		refusal := nomachine.Refusal(model.NodeTypeRun)
		return ExecuteRunStepOutput{Stderr: refusal, Output: refusal, ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds()}, nil
	}

	// Propagate daemon selector from workflow input. Priority: explicit
	// node/workflow-level selector > durable chat pin > worktree owner
	// (set by resolveRunExecutorContext) > default resolution. Override the
	// resolved binding only when the input actually carries a selector, and
	// by the rule ExecuteTools applies: the chat's pin (a session selector)
	// naming another machine than a branch workspace's owner is stale and
	// yields to the owner (toolDaemonSelector).
	if input.DaemonSelector != nil {
		execCtx.DaemonSelector = toolDaemonSelector(execCtx.WorktreeDaemonID, input.DaemonSelector)
	}
	credentials, err := daemonpolicy.ParseCredentials(input.Credentials)
	if err != nil {
		// A typo must not run the command with credentials the author meant to
		// withhold: fail the step as a command verdict, with no retry.
		if retry {
			return refuse()
		}
		msg := "run node: " + err.Error()
		return ExecuteRunStepOutput{Stderr: msg, Output: msg, ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds()}, nil
	}
	execCtx.Credentials = credentials
	execCtx.WritableCheckouts = input.WritableCheckouts

	// The command's machine is final: its checkout is the working directory.
	// A machine without one fails the command with the reason and the way
	// out (exit -1), never a stuck step.
	if err := resolveRunCheckout(ctx, a.repo, a.router, &execCtx); err != nil {
		if retry {
			return refuse()
		}
		refusal := missingRefusal(err)
		return ExecuteRunStepOutput{Stderr: refusal, Output: refusal, ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds()}, nil
	}

	// Determine working directory: the run's checkout, where its log file is
	// resolved, and the directory the command starts in, the node's work_dir
	// when it names one (relative to the checkout).
	workingDir := execCtx.ProjectPath
	worktreeID := execCtx.WorktreeID
	worktreePath := execCtx.WorktreePath
	if worktreePath != "" {
		workingDir = worktreePath
	}
	commandDir := runCommandDir(workingDir, input.WorkDir)
	foreignMachine, foreign := foreignWorkspaceRunMachine(execCtx, commandDir)
	if foreign {
		logging.WarnContext(ctx, "[RunStep] the step's machine selector routes a command in a branch workspace away from the workspace's machine",
			"chat_id", input.ChatID, "step_id", input.StepID, "worktree_id", worktreeID,
			"owner", execCtx.WorktreeDaemonID, "selected", foreignMachine, "command_dir", commandDir)
	}

	// Pick executor: test override or remote via daemon
	executor := a.runExecutor
	if executor == nil {
		remote := NewRemoteRunExecutor(a.toolExecutor)
		remote.SetContext(execCtx)
		executor = remote
	}

	// Execute the command
	wait := a.machineWait
	if wait == nil {
		wait = defaultMachineWait()
	}
	var result CommandResult
	// reattached: the verdict is that of a command reattached to — by a
	// retry, or by this attempt after its transport dropped.
	reattached := retry
	execSpanCtx, execSpan := telemetry.StartRunExec(ctx, telemetry.RunExecStart{Command: input.Command, Attempt: attempt,
		Credentials: credentials, InWorktree: worktreePath != ""})
	if retry {
		reattacher, ok := executor.(RunReattacher)
		if !ok {
			execSpan.End(telemetry.RunExecEnd{Outcome: telemetry.RunOutcomeRetryRefused})
			return refuse()
		}
		var attached bool
		result, attached, err = reattacher.ReattachCommand(execSpanCtx, requestID, timeout)
		if !attached {
			activity.GetLogger(ctx).Info("[RunStep] retry found no command to reattach to; refusing to run it again",
				"chat_id", input.ChatID, "step_id", input.StepID, "attempt", attempt, "request_id", requestID)
			execSpan.End(telemetry.RunExecEnd{Outcome: telemetry.RunOutcomeRetryRefused})
			return refuse()
		}
		activity.GetLogger(ctx).Info("[RunStep] retry reattached to the command the previous attempt started",
			"chat_id", input.ChatID, "step_id", input.StepID, "attempt", attempt, "request_id", requestID,
			"exit_code", result.ExitCode, "error", err)
	} else {
		observe := a.observeMachine(execCtx)
		result, err = executeUntilDelivered(withRunRequestID(execSpanCtx, requestID), executor, wait, observe, input.Command, commandDir, timeout)
		result, reattached, err = reattachAfterTransportDrop(execSpanCtx, executor, input, requestID, timeout, result, err)
	}
	execSpan.End(runExecEnd(ctx, result, err, reattached))
	a.stopCommandIfCancelled(ctx, execCtx, input, requestID)
	stdout, stderr, exitCode, interrupted := result.Stdout, result.Stderr, result.ExitCode, result.Interrupted
	if err == nil && foreign && exitCode != 0 && !interrupted {
		stderr = foreignWorkspaceRunNote(ctx, a.repo, worktreePath, execCtx.WorktreeDaemonID, foreignMachine) + stderr
	}
	if err != nil {
		// The machine never took the command within the wait (or is gone):
		// the step fails saying which machine and for how long.
		var unavailable *machineUnavailableError
		if errors.As(err, &unavailable) {
			activity.GetLogger(ctx).Warn("[RunStep] machine unavailable; failing the step",
				"chat_id", input.ChatID,
				"step_id", input.StepID,
				"daemon_id", unavailable.DaemonID,
				"waited", unavailable.Waited,
				"error", unavailable.Error(),
			)
			// No cause: the message already carries it, and a cause would be
			// appended to it again wherever the error is printed.
			return ExecuteRunStepOutput{}, temporal.NewNonRetryableApplicationError(
				"failed to execute command: "+unavailable.Error(),
				activitytypes.RunStepMachineUnavailableErrType, nil)
		}
		// The step was told to stop (a cancel, a pause, a reset) while it
		// waited for its command, here or in a reattach: whatever the wait
		// returned, the activity ends as that stop, so the workflow stops
		// the step instead of recording a final outcome-unknown failure.
		if stoppedByRequest(ctx) {
			activity.GetLogger(ctx).Info("[RunStep] step stopped while waiting for its command",
				"chat_id", input.ChatID, "step_id", input.StepID, "attempt", attempt,
				"request_id", requestID, "cause", context.Cause(ctx), "error", err)
			return ExecuteRunStepOutput{}, context.Cause(ctx)
		}
		// A transport failure is not a command verdict: fail the activity
		// rather than return a synthetic exit code the workflow would read
		// as one. Not retryable — see runStepTransportFailure — unless this
		// attempt itself was released while it waited (runStepReleased).
		var transportErr *toolexec.TransportError
		if errors.As(err, &transportErr) {
			if attemptReleased(ctx) {
				activity.GetLogger(ctx).Info("[RunStep] attempt released while its command runs; the retry reattaches to it",
					"chat_id", input.ChatID,
					"step_id", input.StepID,
					"attempt", attempt,
					"request_id", requestID,
					"cause", context.Cause(ctx),
				)
				return ExecuteRunStepOutput{}, runStepReleased(transportErr)
			}
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
		_, writeErr := executor.ExecuteCommand(ctx, writeCmd, workingDir, 30000, nil)
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
		Truncated:    result.Truncated,
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

// runStepCancelReason is what the machine is told when a cancelled run step's
// command is stopped.
const runStepCancelReason = "run step cancelled"

// runStepCancelPushTimeout bounds the push that tells the machine to stop a
// cancelled step's command; the activity is already over.
const runStepCancelPushTimeout = 10 * time.Second

// stopCommandIfCancelled tells the machine to stop the step's command when
// Temporal cancelled the step: its run was cancelled (control_run cancel,
// CancelRun, archiving the chat) or paused.
//
// The machine runs the command decoupled from the request that sent it, so
// that a dropped connection cannot kill a healthy command; the cost is that
// nothing about the activity's context reaches it. An agent's tool call is
// stopped by the executor's abandon push, keyed by its tool call id. A run
// step has none, so before this its command ran on to completion after the
// run was cancelled, holding whatever it held. The machine registers the
// command under the request id too, which is what this push names; stopping
// it signals the command's process group.
//
// ONLY a cancel. A worker that stops (a drain, a deploy) or an attempt the
// server timed out also cancels this context, and then the command must keep
// running: the retry reattaches to it by the same request id and returns its
// real result (execute's IDEMPOTENCY note). The SDK tells them apart by the
// cause: a server cancel is a temporal CanceledError; a timed-out attempt is a
// NotFound, a pause ErrActivityPaused and a worker stop a plain cancel.
func (a *ExecuteRunStepActivity) stopCommandIfCancelled(ctx context.Context, execCtx RunExecutorContext, input ExecuteRunStepInput, requestID string) {
	if a.router == nil || requestID == "" || !cancelledByServer(ctx) {
		return
	}
	pushCtx, cancel := context.WithTimeout(toolexec.WithDaemonSelector(context.WithoutCancel(ctx), execCtx.DaemonSelector), runStepCancelPushTimeout)
	defer cancel()
	if err := a.router.SendToolExecutionCancel(pushCtx, execCtx.UserID, requestID, runStepCancelReason); err != nil {
		logging.WarnContext(pushCtx, "[RunStep] could not tell the machine to stop a cancelled step's command; it may run to completion",
			"chat_id", input.ChatID, "step_id", input.StepID, "request_id", requestID, "error", err)
		return
	}
	logging.InfoContext(pushCtx, "[RunStep] step cancelled; told the machine to stop its command",
		"chat_id", input.ChatID, "step_id", input.StepID, "request_id", requestID)
}

// cancelledByServer reports whether ctx ended because Temporal asked for the
// activity to be cancelled (see stopCommandIfCancelled).
func cancelledByServer(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled) && temporal.IsCanceledError(context.Cause(ctx))
}

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
	// runStepReleasedErrType: the attempt was released (its worker stopped)
	// while the command ran. Retryable: the retry reattaches to it.
	runStepReleasedErrType = "RunStepReleased"
)

// A command outlives the attempt waiting for it.
//
// The machine runs every command decoupled from the request that sent it, and
// keeps its result until a gateway acknowledges it (toolexec's
// tool_result_redelivery.go). So "no result came back" is rarely the end:
//
//   - The wait broke but this attempt is still live (a round trip that timed
//     out, a stream that dropped for longer than the router rides out): the
//     attempt asks the machine about the command it sent, by its request id,
//     and waits for that command's real verdict (reattachAfterTransportDrop).
//   - The attempt itself was released while waiting — a worker rollout
//     drains its activities, which is what every prod deploy does to the
//     run step that deploys it (2026-11-19, rc-train's `deploy`): the step
//     fails RETRYABLY (runStepReleased), and the retry reattaches
//     (ReattachCommand). Before, that attempt reported "outcome unknown,
//     retryable: false" and only the worker-shutdown rewrite in the
//     ActivityWrapper kept it from pausing the run.
//
// A machine that cannot vouch for the command — it restarted, no longer holds
// the result, or is too old to be asked (no tool.status) — leaves today's
// verdict: outcome unknown, not retried.

// reattachAfterTransportDrop is the first half of that: given what
// executeUntilDelivered returned, it waits for the command's own verdict when
// the command may have run, no result came back, and this attempt is still
// live. Anything else is returned unchanged. reattached reports that the
// returned verdict is the reattached command's.
func reattachAfterTransportDrop(ctx context.Context, executor RunExecutor, input ExecuteRunStepInput, requestID string, timeoutMs int, result CommandResult, err error) (_ CommandResult, reattached bool, _ error) {
	var transportErr *toolexec.TransportError
	if err == nil || !errors.As(err, &transportErr) || transportErr.NotDispatched || ctx.Err() != nil || requestID == "" {
		return result, false, err
	}
	reattacher, ok := executor.(RunReattacher)
	if !ok {
		return result, false, err
	}
	logger := activity.GetLogger(ctx)
	logger.Info("[RunStep] no result came back; asking the machine about the command it was sent",
		"chat_id", input.ChatID, "step_id", input.StepID, "request_id", requestID,
		"code", transportErr.Code, "detail", transportErr.Detail)
	reattachedResult, attached, reattachErr := reattacher.ReattachCommand(ctx, requestID, timeoutMs)
	if !attached {
		logger.Info("[RunStep] the machine cannot vouch for the command; its outcome is unknown",
			"chat_id", input.ChatID, "step_id", input.StepID, "request_id", requestID)
		return result, false, err
	}
	logger.Info("[RunStep] reattached to the command after the transport dropped",
		"chat_id", input.ChatID, "step_id", input.StepID, "request_id", requestID,
		"exit_code", reattachedResult.ExitCode, "error", reattachErr)
	return reattachedResult, true, reattachErr
}

// attemptReleased reports whether ctx ended because this attempt was let go —
// its worker stopping, a heartbeat RPC that failed, an attempt the server has
// already timed out — rather than because the step was told to stop (a
// cancel, a pause, a reset). Only a released attempt leaves its command to a
// retry.
func attemptReleased(ctx context.Context) bool {
	if !errors.Is(ctx.Err(), context.Canceled) {
		return false
	}
	cause := context.Cause(ctx)
	return !temporal.IsCanceledError(cause) &&
		!errors.Is(cause, activity.ErrActivityPaused) &&
		!errors.Is(cause, activity.ErrActivityReset)
}

// stoppedByRequest reports whether ctx ended because the step was told to
// stop — a server cancel, a pause, a reset — the cancellations attemptReleased
// excludes. The step's own timeout is neither.
func stoppedByRequest(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled) && !attemptReleased(ctx)
}

// runStepReleased is the activity error for an attempt released while its
// command ran: retryable, so the retry reattaches to the command (and is
// refused, as any retry is, if the machine cannot vouch for it).
func runStepReleased(transportErr *toolexec.TransportError) error {
	return temporal.NewApplicationErrorWithOptions(
		"this attempt was released before the command's result came back; the command keeps running on its machine and the retry reattaches to it",
		runStepReleasedErrType, temporal.ApplicationErrorOptions{
			Cause: transportErr,
			// Benign: a worker rollout is not a fault of the step.
			Category: temporal.ApplicationErrorCategoryBenign,
		})
}

// A command that provably never reached a machine (TransportError.
// NotDispatched) is held, inside the attempt that knows it never ran, until
// the machine is back, and then sent again. Only this attempt can do it: a
// Temporal retry starts with no record of how the attempt before it ended, so
// it could not tell this failure from one where the command started.
//
// It is infrastructure, not the command failing, so it spends none of the
// node's retries and does not pause the run. It watches the machine the way
// wait_for_machine does — connected: send again; removed or failed to start:
// stop now, waiting cannot help; anything else (disconnected, restarting,
// starting, asleep): keep waiting — for at most RunStepMachineWaitBudget from
// the first failure, then fails the step naming the machine and the wait.
//
// Each send has already ridden out a short disconnect inside the router
// (DaemonReconnectGrace); this is the longer wait around it, for a machine
// that is away for minutes (a VM restart), not seconds.

// machineWait is the shape of that wait.
type machineWait struct {
	// budget bounds the whole wait, from the first undelivered send.
	budget time.Duration
	// poll is how often a machine that is not attached is looked at again.
	poll time.Duration
	// firstResend and maxResend bound the backoff between sends to a machine
	// that reads attached (or cannot be observed): one that answers the
	// connection check but not the command must not be hammered.
	firstResend, maxResend time.Duration
	// now and sleep are the clock; tests replace them.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

func defaultMachineWait() *machineWait {
	return &machineWait{
		budget:      activitytypes.RunStepMachineWaitBudget,
		poll:        5 * time.Second,
		firstResend: time.Second,
		maxResend:   30 * time.Second,
		now:         time.Now,
		sleep:       sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// machineLook is one look at the run's machine; ok is false when it could not
// be observed (no observer, or the read failed), which is not a verdict.
type machineLook func(ctx context.Context) (status tools.MachineStatus, ok bool)

// observeMachine looks at the machine execCtx's commands are sent to.
func (a *ExecuteRunStepActivity) observeMachine(execCtx RunExecutorContext) machineLook {
	if a.machines == nil {
		return func(context.Context) (tools.MachineStatus, bool) { return tools.MachineStatus{}, false }
	}
	return func(ctx context.Context) (tools.MachineStatus, bool) {
		status, err := a.machines.MachineStatus(toolexec.WithDaemonSelector(ctx, execCtx.DaemonSelector), execCtx.UserID)
		return status, err == nil
	}
}

// machineUnavailableError ends a run step whose command never reached its
// machine: the wait ran out, or the machine is gone. The command never ran.
type machineUnavailableError struct {
	DaemonID string
	Waited   time.Duration
	// Gone is the reason waiting stopped early (removed, failed to start);
	// empty when the budget ran out.
	Gone string
	Last *toolexec.TransportError
}

func (e *machineUnavailableError) Error() string {
	machine := "this run's machine"
	if e.DaemonID != "" {
		machine = "machine " + shortMachineID(e.DaemonID)
	}
	var b strings.Builder
	if e.Gone != "" {
		fmt.Fprintf(&b, "%s %s; the command never ran", machine, e.Gone)
	} else {
		fmt.Fprintf(&b, "%s unavailable for %s; the command never ran", machine, formatMachineWait(e.Waited))
	}
	if e.Last != nil && e.Last.Detail != "" {
		fmt.Fprintf(&b, " (last error: %s)", e.Last.Detail)
	}
	return b.String()
}

func (e *machineUnavailableError) Unwrap() error {
	if e.Last == nil {
		return nil
	}
	return e.Last
}

// shortMachineID is the leading block of a daemon id, as people quote it.
func shortMachineID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	return id
}

// formatMachineWait renders a wait as people say it: "30m", "1h5m", "45s".
func formatMachineWait(d time.Duration) string {
	if d >= time.Minute {
		s := strings.TrimSuffix(d.Round(time.Minute).String(), "0s") // "1h0m", "30m", "1h5m"
		if strings.HasSuffix(s, "h0m") {
			s = strings.TrimSuffix(s, "0m")
		}
		return s
	}
	return d.Round(time.Second).String()
}

// executeUntilDelivered runs command through executor and, while a failure
// proves it never reached a machine, holds it for the machine and sends it
// again (see machineWait). Every other outcome — a verdict, a failure that may
// have followed a start — is returned as it came: it is sent once.
func executeUntilDelivered(ctx context.Context, executor RunExecutor, wait *machineWait, look machineLook, command, workingDir string, timeoutMs int) (result CommandResult, err error) {
	logger := activity.GetLogger(ctx)
	// The wait's clock starts when the FIRST send starts, not when it fails:
	// that send may itself have ridden out a disconnect for up to
	// DaemonReconnectGrace, and the activity's deadline (command timeout +
	// RunStepMachineWaitBudget + RunStepSendOverhead) is measured from the
	// same moment (see types.RunStepSendOverhead).
	first := wait.now()
	waiting := false
	resend := wait.firstResend
	for {
		result, err = executor.ExecuteCommand(ctx, command, workingDir, timeoutMs, nil)
		var transportErr *toolexec.TransportError
		if err == nil || !errors.As(err, &transportErr) || !transportErr.NotDispatched {
			if waiting && err == nil {
				logger.Info("[RunStep] machine is back; the command went through",
					"waited", wait.now().Sub(first).Round(time.Second))
			}
			return result, err
		}
		if !waiting {
			waiting = true
			logger.Warn("[RunStep] command never reached the machine; waiting for it to come back",
				"daemon_id", transportErr.DaemonID, "code", transportErr.Code,
				"budget", wait.budget, "detail", transportErr.Detail)
		}
		unavailable := func(gone string) error {
			return &machineUnavailableError{DaemonID: transportErr.DaemonID, Waited: wait.now().Sub(first), Gone: gone, Last: transportErr}
		}

		// Hold until the machine reads attached, or cannot be observed.
		for {
			remaining := wait.budget - wait.now().Sub(first)
			if remaining <= 0 {
				return CommandResult{ExitCode: -1}, unavailable("")
			}
			status, ok := look(ctx)
			if ok && status.Removed {
				return CommandResult{ExitCode: -1}, unavailable(goneReason("no longer exists", status.StatusMessage))
			}
			if ok && status.Failed {
				return CommandResult{ExitCode: -1}, unavailable(goneReason("failed to start", status.StatusMessage))
			}
			if !ok || status.Connected {
				break
			}
			if wait.sleep(ctx, min(wait.poll, remaining)) != nil {
				return result, err
			}
		}

		remaining := wait.budget - wait.now().Sub(first)
		if remaining <= 0 {
			return CommandResult{ExitCode: -1}, unavailable("")
		}
		logger.Info("[RunStep] sending the command again",
			"daemon_id", transportErr.DaemonID, "retry_in", min(resend, remaining),
			"waited", wait.now().Sub(first).Round(time.Second))
		if wait.sleep(ctx, min(resend, remaining)) != nil {
			return result, err
		}
		resend = min(resend*2, wait.maxResend)
		if wait.now().Sub(first) >= wait.budget {
			return CommandResult{ExitCode: -1}, unavailable("")
		}
		// Never start a send the activity cannot see through: one killed by
		// its deadline is retried by Temporal, refused (RunStepRetryRefused)
		// and pauses the run — exactly what this wait exists to prevent.
		if deadline, ok := ctx.Deadline(); ok &&
			time.Until(deadline) < time.Duration(timeoutMs)*time.Millisecond+activitytypes.RunStepSendOverhead {
			return CommandResult{ExitCode: -1}, unavailable("")
		}
	}
}

func goneReason(what, message string) string {
	if message == "" {
		return what
	}
	return what + " (" + message + ")"
}

// runStepTransportFailure is the activity error for a command whose result
// never came back. Neither kind is retryable: one that never ran was already
// held and sent again by executeUntilDelivered (it reaches here only when the
// wait was cut short), and one that may have run must not be.
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

// runCommandDir is the directory a run node's command starts in: workDir (the
// node's resolved `work_dir`) when it names one, joined onto checkout when it
// is relative; else checkout. A directory that does not exist fails the
// command on its machine, saying so, rather than running it elsewhere.
// foreignWorkspaceRunMachine reports the machine a run node's command is
// pinned to when it is not the machine its branch workspace lives on, and the
// command runs inside that workspace: an explicit node or workflow `daemon:`
// selector keeps its precedence over the workspace's owner
// (toolDaemonSelector), so the command is sent to a machine whose disk most
// likely has no such directory.
//
// The command still runs, unlike an agent's tool calls (ensureBatchWorkspace
// refuses those): a run node could always target another machine, and two
// daemons sharing one disk do have the directory. Refusing would break a
// workflow that works today; the step's failure is explained instead
// (foreignWorkspaceRunNote). A work_dir outside the workspace (an absolute
// path on the selected machine) is not a conflict at all.
func foreignWorkspaceRunMachine(execCtx RunExecutorContext, commandDir string) (string, bool) {
	owner, workspace := execCtx.WorktreeDaemonID, execCtx.WorktreePath
	if owner == "" || workspace == "" || execCtx.DaemonSelector == nil {
		return "", false
	}
	selected := execCtx.DaemonSelector.ID
	if selected == "" || selected == owner || !pathWithin(workspace, commandDir) {
		return "", false
	}
	return selected, true
}

// foreignWorkspaceRunNote leads the stderr of a failed command that ran on
// another machine than its branch workspace's (foreignWorkspaceRunMachine),
// naming both machines, so the failure ("working directory does not exist")
// says why and what to change rather than sending the reader to look for a
// workspace that was never deleted. Only a failure carries it: a command that
// succeeded keeps its output byte for byte.
func foreignWorkspaceRunNote(ctx context.Context, repo any, workspacePath, owner, selected string) string {
	reader, _ := repo.(daemonplacement.DaemonReader)
	ownerName := daemonplacement.MachineName(ctx, reader, owner)
	selectedName := daemonplacement.MachineName(ctx, reader, selected)
	return fmt.Sprintf("[Workspace elsewhere] This step ran on %s, the machine its `daemon:` selector picked, but this chat's workspace %s lives on %s, "+
		"so the command's directory may not exist where it ran. Run the step on %s (or drop the step's `daemon:` selector) to run it in the workspace, "+
		"or give it a work_dir that exists on %s.\n",
		selectedName, workspacePath, ownerName, ownerName, selectedName)
}

func runCommandDir(checkout, workDir string) string {
	workDir = strings.TrimSpace(workDir)
	switch {
	case workDir == "":
		return checkout
	case filepath.IsAbs(workDir) || checkout == "":
		return filepath.Clean(workDir)
	default:
		return filepath.Join(checkout, workDir)
	}
}

// legacyLLMGatewayRefusal is a step declared with the removed llm_gateway's
// verdict: exit -1, its command not run.
const legacyLLMGatewayRefusal = "Error: this run node sets llm_gateway, which was removed (it shipped in v1.29.0): " +
	"the command was meant to run only with an LLM gateway step key, which Reliant no longer issues, so it was not run. " +
	"Remove llm_gateway from the node and run the workflow again"

// hasLegacyLLMGateway reports whether raw is a set llm_gateway (not absent
// or null).
func hasLegacyLLMGateway(raw json.RawMessage) bool {
	v := strings.TrimSpace(string(raw))
	return v != "" && v != "null"
}
