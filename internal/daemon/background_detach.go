// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
)

// The "push to background" seam lives here, next to ClassifyExecOutcome,
// ExecWaitDelay and ExecGraceDelay, for the same reason those do: there are TWO
// exec paths on the daemon and they must not drift.
//
//   - exec.run — the daemon command handler, used when a caller speaks the
//     daemon command protocol directly.
//   - LocalClient.RunCommand — what an LLM tool call actually takes. The daemon
//     runtime hands its local tool executor a LocalClient, so the shell tool's
//     rctx.Daemon.RunCommand lands here.
//
// The probe used to live in daemonruntime and only exec.run consumed it, so the
// path every shell tool call takes ignored the request entirely: the button
// marked the tool_calls row BACKGROUNDED, told the UI it worked, and left the
// command running in the foreground still blocking the workflow.

// backgroundRequestKey carries the per-execution "detach into a background
// process" probe down to whichever exec path runs the command.
//
// A context value rather than a parameter because the daemon command handler
// signature is (ctx, payload) — deliberately narrow — so the request id and the
// daemon client that owns the background registry cannot be passed as arguments
// without changing every handler.
type backgroundRequestKeyType struct{}

var backgroundRequestKey backgroundRequestKeyType

// BackgroundProbe reports whether the user has asked to detach THIS execution,
// returning the LLM tool-call id to attribute the resulting process to. It
// consumes the request, so it fires at most once per execution.
type BackgroundProbe func() (toolCallID string, requested bool)

// WithBackgroundProbe attaches the probe for one command execution.
func WithBackgroundProbe(ctx context.Context, probe BackgroundProbe) context.Context {
	if probe == nil {
		return ctx
	}
	return context.WithValue(ctx, backgroundRequestKey, probe)
}

// backgroundRequested reports whether a background detach was asked for. It is
// false whenever no probe is installed, which is the case for every non-daemon
// caller (server-side executor, tests) — those simply never background.
func backgroundRequested(ctx context.Context) (string, bool) {
	probe, ok := ctx.Value(backgroundRequestKey).(BackgroundProbe)
	if !ok || probe == nil {
		return "", false
	}
	return probe()
}

// BackgroundPollInterval is how often a running command checks whether the user
// asked to detach it. 100ms is imperceptible to a person clicking the button and
// negligible against commands that run long enough to be worth backgrounding.
const BackgroundPollInterval = 100 * time.Millisecond

// DetachOptions describes the running command a detach request would adopt.
type DetachOptions struct {
	Cmd        *exec.Cmd
	Command    string
	WorkingDir string
	StartTime  time.Time
	StdoutBuf  *bytes.Buffer
	StderrBuf  *bytes.Buffer
	// WaitErrCh carries the single cmd.Wait() result. cmd.Wait() may be called
	// exactly once, so on adoption the channel is handed to the background
	// manager rather than waited on twice.
	WaitErrCh chan error
}

// PollForBackgroundDetach waits for the command to finish OR for the user to ask
// that it be detached into a background process, whichever comes first.
//
// Returns (result, true) when the command was adopted into the background
// manager; the caller returns that result immediately and the process keeps
// running under shell_output / shell_kill. Returns (_, false) when the command
// finished on its own, and the caller proceeds with the normal result path —
// having put the wait result back on WaitErrCh for the caller to receive.
func PollForBackgroundDetach(ctx context.Context, opts DetachOptions) (CommandResult, bool) {
	// No probe installed means no caller can ever ask, so skip the ticker
	// entirely rather than waking every 100ms for the whole command.
	if _, ok := ctx.Value(backgroundRequestKey).(BackgroundProbe); !ok {
		return CommandResult{}, false
	}

	ticker := time.NewTicker(BackgroundPollInterval)
	defer ticker.Stop()

	for {
		select {
		case waitErr := <-opts.WaitErrCh:
			// Finished before anyone asked to detach it (or in the same
			// instant). The output is real and complete, so report it normally
			// rather than pretending it was backgrounded.
			//
			// WaitErrCh is buffered with capacity 1 and has exactly one sender,
			// so putting the result back is guaranteed not to block and lets the
			// caller's own receive observe it. cmd.Wait() is still called once.
			opts.WaitErrCh <- waitErr
			return CommandResult{}, false

		case <-ctx.Done():
			return CommandResult{}, false

		case <-ticker.C:
			toolCallID, requested := backgroundRequested(ctx)
			if !requested {
				continue
			}

			process, adoptErr := shell.GetBackgroundManager().AdoptRunningProcess(shell.AdoptRunningProcessOptions{
				Cmd:        opts.Cmd,
				Command:    opts.Command,
				WorkingDir: opts.WorkingDir,
				StartTime:  opts.StartTime,
				StdoutBuf:  opts.StdoutBuf,
				StderrBuf:  opts.StderrBuf,
				WaitErrCh:  opts.WaitErrCh,
			})
			if adoptErr != nil {
				// Adoption failed: the command is still running and still owned
				// by this call, so fall back to waiting for it. Saying so beats
				// reporting a background process that does not exist.
				logging.Warn("[exec] Failed to adopt process into background; continuing in foreground",
					"error", adoptErr, "toolCallID", toolCallID)
				return CommandResult{}, false
			}

			logging.Info("[exec] Detached command into background process",
				"processID", process.ID, "toolCallID", toolCallID)

			out := fmt.Sprintf(
				"Command detached into a background process.\nProcess ID: %s\nCommand: %s\nUse shell_output to read its output and shell_kill to stop it.",
				process.ID, opts.Command)
			resp := CommandResult{
				Stdout:       out,
				DurationMs:   time.Since(opts.StartTime).Milliseconds(),
				ExitCode:     0,
				Backgrounded: true,
				ProcessID:    process.ID,
			}
			resp.Combined = CombineOutput(resp.Stdout, resp.Stderr)
			return resp, true
		}
	}
}
