// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
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
	WorkflowID     string                        `json:"workflow_id"`
	ChatID         string                        `json:"chat_id" reliant:"-"`
	StepID         string                        `json:"step_id"`
	Command        string                        `json:"command"`
	Timeout        int                           `json:"timeout"`                   // in milliseconds, default 5 minutes
	LoopNodeID     string                        `json:"loop_node_id,omitempty"`    // Loop context: which loop node spawned this
	LoopIteration  int                           `json:"loop_iteration"`            // Loop context: iteration index (0-indexed)
	DaemonSelector *activitytypes.DaemonSelector `json:"daemon_selector,omitempty"` // Target daemon for execution
	LogFile        string                        `json:"log_file,omitempty"`        // Redirect stdout+stderr to this file (daemon-side)
	// Credentials is the node's `credentials` setting: "none" runs the command
	// with no GitHub, git, ssh or cloud credentials.
	Credentials string `json:"credentials,omitempty"`
	// SaveMessage delegates the run node's save_message to the ActivityWrapper
	// (see types.SaveMessageRequest). The handler never reads it.
	SaveMessage *activitytypes.SaveMessageRequest `json:"save_message,omitempty" reliant:"-"`
	// SecretRefs are the workflow's declared secrets and where each lives. They
	// are names and environments only: the activity resolves the values itself,
	// exports each as an environment variable of the same name, and masks them
	// in everything it returns. Command carries secret placeholders as markers
	// (stepsecrets.Marker), never values.
	SecretRefs []StepSecretRef `json:"secret_refs,omitempty"`
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
	// machines reports whether the run's machine is attached while a command
	// that never reached it waits (WithMachineObserver). nil: the wait cannot
	// see the machine and re-sends on its backoff alone.
	machines MachineObserver
	// machineWait overrides defaultMachineWait; tests shrink it or give it a
	// fake clock.
	machineWait *machineWait
	// secrets resolves the workflow's declared secrets for the command.
	secrets SecretResolver
	// router resolves a run node's machine named by name, type or labels to
	// the machine whose checkout the command runs in (WithDaemonRouter).
	router toolexec.DaemonRouter
	// capabilities reads whether a secret-bearing command's machine masks
	// them (WithMachineCapabilities).
	capabilities MachineCapabilities
}

// WithSecretResolver lets a run step export the workflow's declared secrets to
// its command. A nil resolver makes a step that declares any fail rather than
// run without them.
func (a *ExecuteRunStepActivity) WithSecretResolver(r SecretResolver) *ExecuteRunStepActivity {
	a.secrets = r
	return a
}

// MachineCapabilities reads what a machine advertised when it registered
// (daemons.capabilities, a JSON array).
type MachineCapabilities interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// WithMachineCapabilities lets a run step that uses secrets check its machine
// masks them (requireSecretMasking). Without it such a step is refused.
func (a *ExecuteRunStepActivity) WithMachineCapabilities(m MachineCapabilities) *ExecuteRunStepActivity {
	a.capabilities = m
	return a
}

// requireSecretMasking resolves the machine execCtx's command goes to, pins
// the command to it, and returns "" when that machine advertises
// toolexec.CapabilitySecretMask, else the refusal to fail the step with. An
// older daemon ignores the mask and writes an echoed secret to its result
// outbox, so anything it cannot be shown to support is refused.
func (a *ExecuteRunStepActivity) requireSecretMasking(ctx context.Context, execCtx *RunExecutorContext) string {
	const refusal = "Error: this step uses the workflow's secrets, and "
	if a.capabilities == nil {
		return refusal + "this worker cannot check that the machine masks them, so the command was not sent."
	}
	daemonID := ""
	if execCtx.DaemonSelector != nil {
		daemonID = execCtx.DaemonSelector.ID
	}
	if daemonID == "" {
		if a.router == nil {
			return refusal + "its machine could not be determined, so the command was not sent."
		}
		id, err := toolexec.ResolveDaemonIDForSelector(ctx, a.router, execCtx.UserID, execCtx.DaemonSelector)
		if err != nil || id == "" {
			return refusal + "its machine could not be determined, so the command was not sent."
		}
		daemonID = id
	}
	d, err := a.capabilities.GetDaemon(ctx, daemonID)
	if err != nil || d == nil || !advertises(d.Capabilities, toolexec.CapabilitySecretMask) {
		return refusal + "machine " + shortMachineID(daemonID) + " runs a Reliant too old to keep them out of its stored results. Update Reliant on that machine and run the step again; the command was not sent."
	}
	execCtx.DaemonSelector = &toolexec.DaemonSelector{ID: daemonID}
	return ""
}

// advertises reports whether a daemon's JSON capability list names want.
func advertises(capsJSON *string, want string) bool {
	if capsJSON == nil {
		return false
	}
	var caps []string
	if json.Unmarshal([]byte(*capsJSON), &caps) != nil {
		return false
	}
	return slices.Contains(caps, want)
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
		msg := "run node: " + err.Error()
		return ExecuteRunStepOutput{Stderr: msg, Output: msg, ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds()}, nil
	}
	execCtx.Credentials = credentials

	// The command's machine is final: its checkout is the working directory.
	// A machine without one fails the command with the reason and the way
	// out (exit -1), never a stuck step.
	if err := resolveRunCheckout(ctx, a.repo, a.router, &execCtx); err != nil {
		refusal := missingRefusal(err)
		return ExecuteRunStepOutput{Stderr: refusal, Output: refusal, ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds()}, nil
	}

	// A command that will carry secrets goes only to a machine that masks
	// them before it stores its result (toolexec.CapabilitySecretMask), and
	// is pinned to that machine, so the one checked is the one it runs on.
	// Checked before anything is resolved: a machine that cannot mask never
	// receives a value, or the command.
	if len(input.SecretRefs) > 0 {
		if refusal := a.requireSecretMasking(ctx, &execCtx); refusal != "" {
			return ExecuteRunStepOutput{Stderr: refusal, Output: refusal, ExitCode: -1,
				Duration: time.Since(startTime).Milliseconds()}, nil
		}
	}

	// Declared secrets: resolved here, inside the attempt, and exported to the
	// command as environment variables of the same name. The command text keeps
	// no value (a placeholder becomes a ${NAME} reference), so a value is not in
	// a command line, a process listing or any stored copy of the command.
	// Everything this step returns is masked before it leaves the activity.
	secrets, secretErr := resolveStepSecrets(ctx, a.secrets, input.WorkflowID, input.StepID, input.SecretRefs)
	if secretErr != nil {
		message, _ := secretFailure(secretErr)
		msg := "Error: " + message
		return ExecuteRunStepOutput{Stderr: msg, Output: msg, ExitCode: -1,
			Duration: time.Since(startTime).Milliseconds()}, nil
	}
	display := stepsecrets.Unprotect(input.Command)
	command := stepsecrets.ShellRefs(input.Command)
	var env map[string]string
	if len(secrets.values) > 0 {
		env = secrets.values.Env()
	}
	mask := secrets.masker.Mask

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
	wait := a.machineWait
	if wait == nil {
		wait = defaultMachineWait()
	}
	observe := a.observeMachine(execCtx)
	stdout, stderr, exitCode, interrupted, err := executeUntilDelivered(ctx, executor, wait, observe, command, workingDir, timeout, env, mask)
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
				"error", mask(unavailable.Error()),
			)
			// No cause: the message already carries it, and a cause would be
			// appended to it again wherever the error is printed.
			return ExecuteRunStepOutput{}, temporal.NewNonRetryableApplicationError(
				"failed to execute command: "+unavailable.Error(),
				activitytypes.RunStepMachineUnavailableErrType, nil)
		}
		// A transport failure is not a command verdict: fail the activity
		// rather than return a synthetic exit code the workflow would read
		// as one. Not retryable either way — see runStepTransportFailure.
		var transportErr *toolexec.TransportError
		if errors.As(err, &transportErr) {
			// The daemon's own words about a failure could echo the command's
			// environment: the log line and the error are built from masked text.
			masked := *transportErr
			masked.Detail = mask(masked.Detail)
			masked.Command = mask(stepsecrets.Unprotect(masked.Command))
			transportErr = &masked
			activity.GetLogger(ctx).Warn("[RunStep] no result came back from the machine",
				"chat_id", input.ChatID,
				"step_id", input.StepID,
				"code", transportErr.Code,
				"never_ran", transportErr.NotDispatched,
				"detail", transportErr.Detail,
			)
			return ExecuteRunStepOutput{}, runStepTransportFailure(transportErr)
		}
		return ExecuteRunStepOutput{}, secrets.masker.MaskError(fmt.Errorf("failed to execute command: %w", err))
	}

	// What leaves the activity is masked first: the output, the log file and
	// the chat update are all built from these.
	stdout, stderr = mask(stdout), mask(stderr)

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
			"command":     display,
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
		"command":            stepsecrets.Unprotect(input.Command),
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
func executeUntilDelivered(ctx context.Context, executor RunExecutor, wait *machineWait, look machineLook, command, workingDir string, timeoutMs int, env map[string]string, mask func(string) string) (stdout, stderr string, exitCode int, interrupted bool, err error) {
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
		stdout, stderr, exitCode, interrupted, err = executor.ExecuteCommand(ctx, command, workingDir, timeoutMs, env)
		var transportErr *toolexec.TransportError
		if err == nil || !errors.As(err, &transportErr) || !transportErr.NotDispatched {
			if waiting && err == nil {
				logger.Info("[RunStep] machine is back; the command went through",
					"waited", wait.now().Sub(first).Round(time.Second))
			}
			return stdout, stderr, exitCode, interrupted, err
		}
		if !waiting {
			waiting = true
			logger.Warn("[RunStep] command never reached the machine; waiting for it to come back",
				"daemon_id", transportErr.DaemonID, "code", transportErr.Code,
				"budget", wait.budget, "detail", mask(transportErr.Detail))
		}
		unavailable := func(gone string) error {
			// The daemon's words are masked here, at the one place this error
			// is built, so no later print of it can carry a value.
			last := *transportErr
			last.Detail = mask(last.Detail)
			last.Command = mask(last.Command)
			return &machineUnavailableError{DaemonID: transportErr.DaemonID, Waited: wait.now().Sub(first), Gone: gone, Last: &last}
		}

		// Hold until the machine reads attached, or cannot be observed.
		for {
			remaining := wait.budget - wait.now().Sub(first)
			if remaining <= 0 {
				return "", "", -1, false, unavailable("")
			}
			status, ok := look(ctx)
			if ok && status.Removed {
				return "", "", -1, false, unavailable(goneReason("no longer exists", status.StatusMessage))
			}
			if ok && status.Failed {
				return "", "", -1, false, unavailable(goneReason("failed to start", status.StatusMessage))
			}
			if !ok || status.Connected {
				break
			}
			if wait.sleep(ctx, min(wait.poll, remaining)) != nil {
				return stdout, stderr, exitCode, interrupted, err
			}
		}

		remaining := wait.budget - wait.now().Sub(first)
		if remaining <= 0 {
			return "", "", -1, false, unavailable("")
		}
		logger.Info("[RunStep] sending the command again",
			"daemon_id", transportErr.DaemonID, "retry_in", min(resend, remaining),
			"waited", wait.now().Sub(first).Round(time.Second))
		if wait.sleep(ctx, min(resend, remaining)) != nil {
			return stdout, stderr, exitCode, interrupted, err
		}
		resend = min(resend*2, wait.maxResend)
		if wait.now().Sub(first) >= wait.budget {
			return "", "", -1, false, unavailable("")
		}
		// Never start a send the activity cannot see through: one killed by
		// its deadline is retried by Temporal, refused (RunStepRetryRefused)
		// and pauses the run — exactly what this wait exists to prevent.
		if deadline, ok := ctx.Deadline(); ok &&
			time.Until(deadline) < time.Duration(timeoutMs)*time.Millisecond+activitytypes.RunStepSendOverhead {
			return "", "", -1, false, unavailable("")
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
