// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type BashWaitParams struct {
	ProcessID string `json:"process_id" jsonschema:"required,description=The ID of the background process to wait for"`
	// TimeoutSeconds is how long to block before returning "still running".
	// It is a bound on THIS call, not a deadline for the process: timing out
	// leaves the process alive and is not an error.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" jsonschema:"description=Maximum seconds to block before returning a still-running result (default: 1200, maximum: 1200). Timing out does NOT kill the process — call again to keep waiting."`
	// TailLines controls how much of the finished process's output comes back
	// with the exit code, so the common case needs no follow-up bash_output.
	TailLines int `json:"tail_lines,omitempty" jsonschema:"description=Lines of output to return when the process exits (default: 50, 0 for none). Use bash_output for the full log or for regex filtering."`
}

type BashWaitResponseMetadata struct {
	ProcessID string `json:"process_id"`
	Status    string `json:"status"`
	Command   string `json:"command"`
	HasExited bool   `json:"has_exited"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	// WaitedMs is how long this call blocked, so a caller can tell a process
	// that finished immediately from one that took the whole budget.
	WaitedMs int64 `json:"waited_ms"`
	// TimedOut reports that the budget elapsed with the process still running.
	// Distinct from an error: the process is fine and the call can be repeated.
	TimedOut bool `json:"timed_out"`
}

type bashWaitTool struct{}

// MaxBlockingToolWait is the longest a single blocking tool call may park.
// toolexec.DefaultToolTimeout is derived from it, adding headroom so the
// tool returns its own "still running" answer before the executor cancels it.
const MaxBlockingToolWait = 20 * time.Minute

const (
	BashWaitToolName = "bash_wait"

	// bashWaitDefaultTimeout is the default block budget.
	//
	// The tool-execution context cancels every tool call at
	// toolexec.DefaultToolTimeout, which is DERIVED from this value (it adds a
	// minute of headroom) so the two can never drift into the configuration
	// where the ceiling fires first. That headroom is what lets this tool
	// write a real answer — "still running, call again" — instead of being
	// cancelled mid-flight and surfacing as a hard timeout error, which is
	// exactly the failure `sleep 300` produced.
	//
	// 4 minutes was too short for the builds and test suites this tool exists
	// to wait on: a 10-minute suite cost three round-trips of pure waiting,
	// which is the polling cost bash_wait was built to remove.
	bashWaitDefaultTimeout = MaxBlockingToolWait
	bashWaitMaxTimeout     = MaxBlockingToolWait

	// bashWaitPollInterval trades responsiveness against daemon chatter. The
	// wait is server-side, so this costs no model round-trips: it is a loop in
	// one tool call, not one call per poll.
	bashWaitPollInterval = 500 * time.Millisecond

	bashWaitDefaultTailLines = 50

	bashWaitDescription = `Block until a background process exits, then return its exit code and recent output.

WHY THIS EXISTS:
Waiting by running a sleep command is the wrong tool and costs far more than it
looks. ` + "`sleep 300; tail log`" + ` occupies a whole turn doing nothing, and it
frequently exceeds the tool timeout and dies, losing the wait entirely. Polling
bash_output in a loop is better but spends a model round-trip on every check.
bash_wait blocks server-side: one tool call, no round-trips, no lost work.

WHEN TO USE:
- Waiting for a long build, test suite, or install to finish
- Any time the next thing you do depends on a background process being done

WHEN NOT TO USE:
- A long-running server you never expect to exit (use bash_output to check on it)
- You only want progress so far, not completion (use bash_output)

HOW TO USE:
1. Start the work in the background:
   bash(command="npm test", run_in_background=true)
2. Do any useful work that does not depend on the result — read the next file,
   prepare the following edit. The process runs while you do.
3. Wait for it:
   bash_wait(process_id="<id>")

TIMEOUTS ARE NOT FAILURES:
If the process is still running when the budget elapses, this returns normally
with timed_out: true and the process untouched. Call bash_wait again to keep
waiting. It never kills the process — use bash_kill for that.

Because a single call cannot block past the tool-execution ceiling, a very long
build may need a few consecutive bash_wait calls. That is still dramatically
cheaper than polling, and unlike a sleep it cannot lose the wait.

RETURNS:
- Exit code and status once the process has exited
- The last tail_lines lines of output (default 50), so a passing build or a
  failing test usually needs no follow-up call
- timed_out: true, with no exit code, if the budget elapsed first

Use bash_output for the full log, for pagination, or for regex filtering.`
)

func NewBashWaitTool() Tool {
	tool := &bashWaitTool{}
	return NewToolWrapper[BashWaitParams, ToolResponse](tool)
}

func (b *bashWaitTool) Name() string {
	return BashWaitToolName
}

func (b *bashWaitTool) Description() string {
	return bashWaitDescription
}

func (b *bashWaitTool) RequiresPermission(params BashWaitParams) (bool, error) {
	// Waiting observes a process that is already running; it starts nothing and
	// changes nothing. Read-only, like bash_output.
	return false, nil
}

func (b *bashWaitTool) Execute(rctx *rctx.ToolContext, params BashWaitParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("command execution requires a connected daemon"), nil
	}
	if params.ProcessID == "" {
		return NewTextErrorResponse("process_id is required"), nil
	}

	budget := bashWaitDefaultTimeout
	if params.TimeoutSeconds > 0 {
		budget = time.Duration(params.TimeoutSeconds) * time.Second
		if budget > bashWaitMaxTimeout {
			// Clamp rather than reject: the caller wants to wait longer, and
			// the right answer is to wait as long as one call allows and tell
			// them to call again — not to fail and make them guess a number.
			budget = bashWaitMaxTimeout
		}
	}

	tailLines := bashWaitDefaultTailLines
	if params.TailLines != 0 {
		tailLines = params.TailLines
	}
	if tailLines < 0 {
		tailLines = 0
	}

	// Confirm the process exists before committing to a long block. Blocking
	// the full budget on a typo'd id, only to report "not found", wastes
	// exactly the time this tool exists to save.
	proc, err := findProcess(rctx, params.ProcessID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list processes: %v", err)), nil
	}
	if proc == nil {
		return NewTextErrorResponse(fmt.Sprintf(
			"No process with ID %q. Use bash_list to see processes in this workspace.", params.ProcessID)), nil
	}

	start := time.Now()
	deadline := start.Add(budget)

	for {
		if proc.Status != "running" {
			return b.exitedResponse(rctx, proc, tailLines, time.Since(start))
		}

		if time.Now().After(deadline) {
			return b.stillRunningResponse(proc, time.Since(start))
		}

		// Honour cancellation of the surrounding tool call. Without this the
		// loop would keep polling a daemon for a call nobody is waiting on.
		select {
		case <-rctx.Done():
			return b.stillRunningResponse(proc, time.Since(start))
		case <-time.After(bashWaitPollInterval):
		}

		refreshed, refreshErr := findProcess(rctx, params.ProcessID)
		if refreshErr != nil {
			// A transient listing failure is not a reason to abandon a wait
			// that may be nearly done; keep the last known state and retry.
			continue
		}
		if refreshed == nil {
			// The process disappeared from the list (e.g. reaped). Report what
			// was last known rather than claiming it is still running.
			return b.vanishedResponse(proc, time.Since(start))
		}
		proc = refreshed
	}
}

func (b *bashWaitTool) exitedResponse(rctx *rctx.ToolContext, proc *daemon.ProcessInfo, tailLines int, waited time.Duration) (ToolResponse, error) {
	exitCodeStr := "unknown"
	if proc.ExitCode != nil {
		exitCodeStr = fmt.Sprintf("%d", *proc.ExitCode)
	}

	output := fmt.Sprintf("Process %s %s after %s (exit code: %s)\nCommand: %s",
		proc.ID, proc.Status, formatWaitDuration(waited), exitCodeStr, proc.Command)

	if tailLines > 0 {
		procOutput, err := rctx.Daemon.GetProcessOutput(rctx.Context, proc.ID, &daemon.OutputOpts{
			TailLines: tailLines,
		})
		if err == nil && procOutput != nil && procOutput.Output != "" {
			output += fmt.Sprintf("\n\n=== LAST %d LINES ===\n%s", tailLines, procOutput.Output)
		} else if err == nil {
			output += "\n\n(no output)"
		}
	}

	metadata := BashWaitResponseMetadata{
		ProcessID: proc.ID,
		Status:    proc.Status,
		Command:   proc.Command,
		HasExited: true,
		ExitCode:  proc.ExitCode,
		WaitedMs:  waited.Milliseconds(),
	}
	return WithResponseMetadata(NewTextResponse(output), metadata), nil
}

// stillRunningResponse is a SUCCESSFUL result, not an error: the process is
// healthy and the only news is that it is not done yet. Returning an error here
// would train the model to treat a slow build as a failure.
func (b *bashWaitTool) stillRunningResponse(proc *daemon.ProcessInfo, waited time.Duration) (ToolResponse, error) {
	output := fmt.Sprintf(
		"Process %s is STILL RUNNING after %s — not an error, and it has not been killed.\nCommand: %s\n\nCall bash_wait again to keep waiting, or bash_output to see progress so far.",
		proc.ID, formatWaitDuration(waited), proc.Command)

	metadata := BashWaitResponseMetadata{
		ProcessID: proc.ID,
		Status:    proc.Status,
		Command:   proc.Command,
		HasExited: false,
		WaitedMs:  waited.Milliseconds(),
		TimedOut:  true,
	}
	return WithResponseMetadata(NewTextResponse(output), metadata), nil
}

func (b *bashWaitTool) vanishedResponse(proc *daemon.ProcessInfo, waited time.Duration) (ToolResponse, error) {
	output := fmt.Sprintf(
		"Process %s is no longer listed after %s; it ended and its record was removed, so no exit code is available.\nCommand: %s",
		proc.ID, formatWaitDuration(waited), proc.Command)

	metadata := BashWaitResponseMetadata{
		ProcessID: proc.ID,
		Status:    "unknown",
		Command:   proc.Command,
		HasExited: true,
		WaitedMs:  waited.Milliseconds(),
	}
	return WithResponseMetadata(NewTextResponse(output), metadata), nil
}

// findProcess returns the process with the given id, or nil when no such
// process is listed. A nil result is not an error: "not listed" is a real
// answer, distinct from "the listing failed", and the two must go different
// ways at every call site.
func findProcess(rctx *rctx.ToolContext, processID string) (*daemon.ProcessInfo, error) {
	processes, err := rctx.Daemon.ListProcesses(rctx.Context)
	if err != nil {
		return nil, err
	}
	for _, p := range processes {
		if p != nil && p.ID == processID {
			return p, nil
		}
	}
	return nil, nil
}

func formatWaitDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}
