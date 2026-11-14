// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// RunExecutor defines the interface for executing shell commands.
// This abstraction allows for mocking in tests.
type RunExecutor interface {
	// ExecuteCommand runs a command in the specified working directory with
	// the given timeout and returns what it printed and how it ended.
	ExecuteCommand(
		ctx context.Context,
		command string,
		workingDir string,
		timeoutMs int,
		env map[string]string,
	) (CommandResult, error)
}

// CommandResult is one command's verdict.
type CommandResult struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	Interrupted bool
	// Truncated reports that the machine cut stdout or stderr to its output
	// cap (head+tail, with a marker in the middle): what is here is not the
	// whole output, and text a workflow parses (json.decode) may not decode.
	Truncated bool
}

// RunExecutorContext provides the IDs needed to build a ToolRequest for remote execution.
// Set by the activity before calling ExecuteCommand.
type RunExecutorContext struct {
	UserID    string
	ChatID    string
	ProjectID string
	// OrgID is the chat's org, whose machines policy governs the command
	// (toolexec.ToolRequest.OrgID); empty outside any org.
	OrgID string
	// ProjectPath is the project's checkout on the machine the command runs
	// on (resolveRunCheckout), never projects.path.
	ProjectPath  string
	ProjectName  string
	WorktreeID   string
	WorktreePath string
	// WorktreeDaemonID is the machine holding the chat's branch workspace
	// (worktrees.daemon_id); empty for the main checkout or an unattributed
	// row.
	WorktreeDaemonID string
	DaemonSelector   *toolexec.DaemonSelector // Target daemon for execution (optional)
	// NoMachine is the chat's no-machine fact: a command has nowhere to run.
	NoMachine bool
	// Credentials is the run node's `credentials` setting ("none" strips
	// GitHub, git, ssh and cloud credentials from the command's environment).
	Credentials string
	// WritableCheckouts is the run node's `writable_checkouts`.
	WritableCheckouts []string

	// project is the chat's project, for resolveRunCheckout.
	project *db.Project
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

// shellInput is the JSON structure the shell tool expects.
type shellInput struct {
	Command     string `json:"command"`
	Timeout     int    `json:"timeout,omitempty"` // timeout in milliseconds
	Credentials string `json:"credentials,omitempty"`
	// WritableCheckouts: a shell tool older than the field ignores it.
	WritableCheckouts []string `json:"writable_checkouts,omitempty"`
}

// RunReattacher is a RunExecutor that can wait for a command an earlier
// attempt of the same run step sent under requestID (runStepRequestID),
// without sending it again. RemoteRunExecutor is one.
type RunReattacher interface {
	// ReattachCommand returns the verdict of the command sent as requestID.
	// attached is false when there is no such command to wait for — the
	// machine does not know it, no longer holds its result, or cannot be
	// asked — and then nothing ran and nothing was waited for.
	ReattachCommand(ctx context.Context, requestID string, timeoutMs int) (result CommandResult, attached bool, err error)
}

// toolReattacher is the ToolExecutor half of RunReattacher:
// toolexec.RemoteExecutor.
type toolReattacher interface {
	ReattachTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, bool, error)
}

// The worker's executor reattaches; a wrapper that hid ReattachTool would
// quietly turn every reattach back into a refusal.
var (
	_ RunReattacher  = (*RemoteRunExecutor)(nil)
	_ toolReattacher = (*toolexec.RemoteExecutor)(nil)
)

// runRequestIDKey carries the transport id of a run step's command on ctx,
// from ExecuteRunStep to RemoteRunExecutor.ExecuteCommand. On ctx rather than
// the executor so only the step's own command carries it: the log-file write
// that follows is another command, with a fresh id of its own.
type runRequestIDKey struct{}

func withRunRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, runRequestIDKey{}, requestID)
}

func runRequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(runRequestIDKey{}).(string)
	return id
}

// ExecuteCommand routes a shell command through the daemon's shell tool.
func (e *RemoteRunExecutor) ExecuteCommand(
	ctx context.Context,
	command string,
	workingDir string,
	timeoutMs int,
	env map[string]string,
) (CommandResult, error) {
	req, err := e.toolRequest(command, workingDir, timeoutMs, env)
	if err != nil {
		return CommandResult{ExitCode: -1}, err
	}
	req.RequestID = runRequestIDFrom(ctx)

	// Execute through the daemon
	result, err := e.executor.ExecuteTool(ctx, req)
	if err != nil {
		return CommandResult{ExitCode: -1}, fmt.Errorf("tool execution failed: %w", err)
	}
	return commandResult(command, result)
}

// ReattachCommand implements RunReattacher over a ToolExecutor that can
// reattach (toolexec.RemoteExecutor.ReattachTool). The verdict is mapped as
// ExecuteCommand maps one.
func (e *RemoteRunExecutor) ReattachCommand(ctx context.Context, requestID string, timeoutMs int) (CommandResult, bool, error) {
	reattacher, ok := e.executor.(toolReattacher)
	if !ok || requestID == "" {
		return CommandResult{}, false, nil
	}
	// Only the ids and the selector address the call; the input is never
	// sent. RunStep keeps the result's content whole, as it was for the
	// attempt that sent it.
	req, err := e.toolRequest("", "", timeoutMs, nil)
	if err != nil {
		return CommandResult{}, false, nil
	}
	req.RequestID = requestID
	result, attached, err := reattacher.ReattachTool(ctx, req)
	if err != nil {
		return CommandResult{ExitCode: -1}, attached, fmt.Errorf("tool execution failed: %w", err)
	}
	if !attached {
		return CommandResult{}, false, nil
	}
	res, err := commandResult("", result)
	return res, true, err
}

// toolRequest is the shell tool request that runs command.
func (e *RemoteRunExecutor) toolRequest(command, workingDir string, timeoutMs int, env map[string]string) (*toolexec.ToolRequest, error) {
	// Build shell tool input
	input := shellInput{
		Command:           command,
		Timeout:           timeoutMs,
		Credentials:       e.execCtx.Credentials,
		WritableCheckouts: e.execCtx.WritableCheckouts,
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal shell tool input: %w", err)
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
		ToolName:       names.ToolShell,
		ToolInput:      string(inputJSON),
		UserID:         e.execCtx.UserID,
		ChatID:         e.execCtx.ChatID,
		ProjectID:      e.execCtx.ProjectID,
		OrgID:          e.execCtx.OrgID,
		WorktreeID:     e.execCtx.WorktreeID,
		ProjectPath:    e.execCtx.ProjectPath,
		ProjectName:    e.execCtx.ProjectName,
		WorktreePath:   e.execCtx.WorktreePath,
		WorkingDir:     effectiveWorkingDir,
		Timeout:        time.Duration(timeoutMs) * time.Millisecond,
		Environment:    env,
		DaemonSelector: e.execCtx.DaemonSelector,
		// A run node's command, not a model's call: the shell tool lets
		// timeoutMs (the node's declared timeout) bound it instead of the
		// model's 10-minute foreground cap. Set here and nowhere else.
		RunStep: true,
	}
	return req, nil
}

// commandResult maps the shell tool's result for command back to the
// RunExecutor contract.
func commandResult(command string, result *toolexec.ToolResult) (CommandResult, error) {
	// Map tool result back to RunExecutor contract
	if result == nil {
		return CommandResult{ExitCode: -1}, fmt.Errorf("nil result from tool executor")
	}

	if result.IsError {
		// A transport failure is NOT a command verdict. Reporting it as exit 1
		// with empty stdout is indistinguishable from a real lint/test/build
		// failure, so a gate "fails" on work that was never attempted, burns a
		// retry, and points the agent at a log file that was never written (or
		// worse, at the previous iteration's). Surface it as an error so the
		// activity's retry policy — not the workflow's attempt budget — owns it.
		if toolexec.IsTransportErrorCode(result.ErrorCode) {
			return CommandResult{ExitCode: -1}, &toolexec.TransportError{
				Code:          result.ErrorCode,
				Command:       command,
				Detail:        result.Content,
				NotDispatched: result.NotDispatched,
				DaemonID:      result.DaemonID,
			}
		}
		// The tool itself reported an error (bad input, tool not found, …):
		// that IS a verdict about this command.
		return CommandResult{Stderr: result.Content, ExitCode: 1}, nil
	}

	// The shell tool returns structured JSON: {"stdout": "...", "stderr": "...", "exit_code": 0}
	var shellOutput struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(result.Content), &shellOutput); err != nil {
		// Fallback: treat content as plain text stdout (e.g. legacy format)
		return CommandResult{Stdout: result.Content, Truncated: shellOutputTruncated(result.Metadata)}, nil
	}

	return CommandResult{
		Stdout:    shellOutput.Stdout,
		Stderr:    shellOutput.Stderr,
		ExitCode:  shellOutput.ExitCode,
		Truncated: shellOutputTruncated(result.Metadata),
	}, nil
}

// shellOutputTruncated reads the shell tool's own report that it cut the
// command's output (ShellResponseMetadata.Truncated). Every daemon release
// sends it, so a run step is told about a cut even on a machine whose shell
// tool still applies the model's 16 KB cap to run steps.
func shellOutputTruncated(metadata string) bool {
	if metadata == "" {
		return false
	}
	var m struct {
		Truncated bool `json:"truncated"`
	}
	return json.Unmarshal([]byte(metadata), &m) == nil && m.Truncated
}

// resolveRunExecutorContext loads the IDs needed for remote execution from the DB.
func resolveRunExecutorContext(ctx context.Context, repo RunExecutorContextStore, chatID string) (RunExecutorContext, error) {
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

	// ProjectPath is resolved once the command's machine is final
	// (resolveRunCheckout): it is that machine's checkout.
	execCtx := RunExecutorContext{
		UserID:      project.UserID,
		ChatID:      chatID,
		ProjectID:   project.ID,
		ProjectName: project.Name,
		NoMachine:   chat.NoMachine,
		project:     project,
	}
	if chat.OrgID != nil {
		execCtx.OrgID = *chat.OrgID
	}

	// Check if chat has a worktree
	worktreeOwner := ""
	if chat.WorktreeID != nil && *chat.WorktreeID != "" {
		worktree, err := repo.GetWorktree(ctx, *chat.WorktreeID)
		if err != nil {
			return RunExecutorContext{}, fmt.Errorf("worktree %s lookup failed: %w", *chat.WorktreeID, err)
		}
		execCtx.WorktreeID = worktree.ID
		// The main worktree row mirrors projects.path: the main checkout
		// is resolved on the command's machine instead.
		if !worktree.IsMain {
			execCtx.WorktreePath = worktree.Path
			worktreeOwner = ptrValue(worktree.DaemonID)
		}
	}
	execCtx.WorktreeDaemonID = worktreeOwner
	// Live workflow snapshots can predate launch-time pinning. The durable
	// chat pin, not a newly connected default, places commands and log writes.
	// A chat worktree's owner is the fallback when the chat has no pin; an
	// explicit node/workflow selector overrides both (run_step.go).
	execCtx.DaemonSelector = toolDaemonSelector(worktreeOwner, chatDaemonSelector(chat, nil))

	return execCtx, nil
}

// resolveRunCheckout fills execCtx.ProjectPath with the project's checkout on
// the machine the command runs on, and pins the command there, so the path
// and the machine agree. The machine is the selector's: an id names it, a
// name, type or labels is resolved through router the way dispatch resolves
// it (configMachine), and no selector at all is the machine the project's
// work is placed on. A selector router cannot resolve keeps routing as it
// does, with the project's placed checkout as its path.
//
// A command in the main checkout of a machine without one returns
// projectcheckout's refusal (the machines that have it, or cloning it there):
// the step reports it as a failed command, never a stuck run. A branch
// worktree's command runs at the worktree, so its machine's checkout is only
// the project root it is told, best effort.
func resolveRunCheckout(ctx context.Context, r projectcheckout.Reader, router toolexec.DaemonRouter, execCtx *RunExecutorContext) error {
	if execCtx.project == nil {
		return nil
	}
	daemonID := configMachine(ctx, router, execCtx.UserID, execCtx.DaemonSelector)
	target, err := projectcheckout.ForMachine(ctx, r, machinesOf(router), execCtx.UserID, execCtx.project, daemonID, checkoutPurpose)
	if err != nil {
		if execCtx.WorktreePath != "" {
			return nil
		}
		return err
	}
	execCtx.ProjectPath = target.Path
	if execCtx.DaemonSelector == nil || (execCtx.DaemonSelector.ID == "" && daemonID != "") {
		execCtx.DaemonSelector = &toolexec.DaemonSelector{ID: target.DaemonID}
	}
	return nil
}
