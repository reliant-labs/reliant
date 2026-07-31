// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/reliant-labs/reliant/internal/cgroupmem"
)

// TimeoutExitCode is the conventional shell exit status for a command killed
// by a timeout (what coreutils `timeout` reports).
const TimeoutExitCode = 124

// ExecWaitDelay bounds how long cmd.Wait() may block AFTER the command's own
// process has exited (or been killed) waiting for its output pipes to close.
// Both exec paths read it, so neither can bound the wait differently.
//
// Why it has to exist: both paths set cmd.Stdout to a *bytes.Buffer rather
// than an *os.File, so os/exec creates an OS pipe and copies from it in a
// goroutine. cmd.Wait() does not return until that copy sees EOF, and EOF
// arrives only when EVERY process holding the write end has exited — not just
// the shell we spawned. Any grandchild that outlives the shell holds it open:
// a `next build` worker, a backgrounded `&` job, a daemonized helper. With no
// WaitDelay that wait is unbounded, and it is unbounded in two ways:
//
//   - A command that finishes its work in seconds returns only when the last
//     grandchild exits, reporting exit 0 and a wall-clock nothing explains.
//   - TimeoutMs stops bounding anything, because killing the shell does not
//     close a pipe a surviving grandchild still holds.
//
// What the bound costs when it fires: output not yet written at that moment is
// lost. Everything written before it is kept, and the command's own exit
// status is kept. Nothing else changes — the lingering process is deliberately
// left running (killing the process group would take down deliberately
// daemonized work and could corrupt a build mid-write; the hang is the defect,
// process lifetime is not).
//
// Why ten seconds: the legitimate post-exit drain is a memory copy of at most
// one pipe buffer, and reaping a SIGKILLed process is a scheduler round —
// both milliseconds. Ten seconds is three orders of magnitude of headroom for
// both, including on a machine under the memory pressure this package's OOM
// accounting exists to survive, while turning an unbounded hang into a bounded
// overshoot. It is also small against the 60s default command timeout, so it
// cannot silently eat a meaningful slice of the budget it protects. Critically
// it is NOT a cap on how long a command may run: WaitDelay's clock starts only
// once the process has exited or the context is done, so a command that is
// merely slow is never cut short no matter how long it takes.
//
// A var rather than a const only so tests can shorten it; production never
// reassigns it.
var ExecWaitDelay = 10 * time.Second

// LingeringOutputMessage explains a WaitDelay expiry in the one channel every
// consumer already reads. Phrased for the reader who is looking at a command
// that worked: the failure is in the reporting, not in the command.
const LingeringOutputMessage = "command finished, but output collection was cut off: a process it started outlived it and held the output pipe open. Output above is complete up to that point; anything written after it is missing. The lingering process was left running — start long-lived processes as background processes rather than with a trailing '&'."

// OOMChecker is the slice of *cgroupmem.Reader that outcome classification
// needs. It is a parameter rather than a package-level reader so each exec
// path keeps its own reader (and its tests keep swapping it).
type OOMChecker interface {
	CheckOOMKill(exitCode int, snap cgroupmem.OOMSnapshot) (bool, string)
}

// ExecOutcome is the classified result of running a child process: the exit
// code to report, why it died, and the stderr the caller should return.
type ExecOutcome struct {
	ExitCode  int
	TimedOut  bool
	OOMKilled bool
	// OutputIncomplete is true when the command ran to completion but output
	// collection was cut off at ExecWaitDelay because a process it spawned
	// outlived it holding the output pipe open.
	OutputIncomplete bool
	// Stderr is the child's captured stderr, plus any explanation the child
	// could not write itself (a start failure, an OOM kill, a cut-off drain).
	Stderr string
}

// ClassifyExecOutcome turns the error from cmd.Start()/cmd.Wait() into the
// exit code, flags, and stderr a command result reports.
//
// The rule that matters: an error that is NOT an *exec.ExitError means the
// child never ran to completion, so nothing was ever written to its stderr
// pipe. chdir into a missing WorkingDir, fork/exec failures, permission
// denied, and a missing shell binary all land here. Dropping that error
// returns {stdout:"", stderr:"", exit_code:1} for EVERY command in that
// environment — including `echo hello` — which is indistinguishable from a
// command that ran and printed nothing, and leaves a caller no way to tell
// "your directory is gone" from "your command produced no output". So the
// error text is appended to stderr: it is the only channel the caller has.
//
// cmdErr is the error from Start/Wait; ctxErr is the exec context's Err()
// (context.DeadlineExceeded marks a timeout); stderr is the child's captured
// stderr so far.
func ClassifyExecOutcome(cmdErr, ctxErr error, stderr string, mem OOMChecker, snap cgroupmem.OOMSnapshot) ExecOutcome {
	out := ExecOutcome{Stderr: stderr}
	if cmdErr == nil {
		return out
	}

	out.TimedOut = errors.Is(ctxErr, context.DeadlineExceeded)

	// ErrWaitDelay means the ExecWaitDelay bound fired: the command's process
	// was already gone and only its output pipes were still held open by
	// something it spawned.
	//
	// os/exec surfaces this error from Wait() ONLY when the process "otherwise
	// exited normally on its own" — a non-zero status becomes an *exec.ExitError,
	// which takes precedence (see (*exec.Cmd).Wait). So reaching here means the
	// command SUCCEEDED. Letting it fall through to the generic non-ExitError
	// branch below would report exit 1 and paste "exec: WaitDelay expired
	// before I/O complete" onto stderr, inventing a failure that never
	// happened — the exact class of mistake this file exists to prevent.
	//
	// A timeout keeps precedence: if the deadline fired, that is what the
	// caller needs to know, and an incidental drain overrun must not downgrade
	// it to success.
	if !out.TimedOut && errors.Is(cmdErr, exec.ErrWaitDelay) {
		out.OutputIncomplete = true
		out.Stderr = appendStderr(out.Stderr, LingeringOutputMessage)
		return out
	}

	var exitErr *exec.ExitError
	if errors.As(cmdErr, &exitErr) {
		// The child ran and reported a status; its own stderr is the
		// explanation and needs no annotation from us.
		out.ExitCode = exitErr.ExitCode()
	} else {
		if out.TimedOut {
			out.ExitCode = TimeoutExitCode
		} else {
			out.ExitCode = 1
		}
		out.Stderr = appendStderr(out.Stderr, cmdErr.Error())
	}

	// A SIGKILL-shaped failure (exit -1: shell itself killed; exit 137: shell
	// reports a killed child) that coincides with an oom_kill in the container
	// cgroup gets the structured out-of-memory explanation appended, so the
	// LLM tool result and user RPC consumers both see actionable text plus a
	// flag for programmatic handling. Timeouts also SIGKILL — TimedOut keeps
	// precedence, so they are never relabelled as OOM.
	if !out.TimedOut && mem != nil {
		if oom, msg := mem.CheckOOMKill(out.ExitCode, snap); oom {
			out.OOMKilled = true
			out.Stderr = appendStderr(out.Stderr, msg)
		}
	}

	return out
}

// appendStderr joins an explanation onto captured stderr, newline-separated,
// without leading a blank line when there was no captured stderr.
func appendStderr(stderr, msg string) string {
	if msg == "" {
		return stderr
	}
	if stderr == "" {
		return msg
	}
	return stderr + "\n" + msg
}

// CombineOutput joins stdout and stderr into the single chronological-ish
// blob CommandResult.Combined carries.
func CombineOutput(stdout, stderr string) string {
	if stderr == "" {
		return stdout
	}
	if stdout == "" {
		return stderr
	}
	return stdout + "\n" + stderr
}
