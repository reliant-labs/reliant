// Copyright (c) 2025 Reliant Labs
package osutil

import (
	"os"
	"os/exec"
	"sync"
	"time"
)

// DefaultGraceDelay is how long a cancelled child gets to exit on its own
// after SIGTERM before it is killed outright.
//
// Three seconds is the budget for a signal handler that does bounded cleanup:
// git unlinking .git/index.lock, a build tool removing a partial artifact, a
// test runner flushing a report. Those are file removals and buffer flushes —
// milliseconds of work — so this is three orders of magnitude of headroom
// while still bounding a cancel a user is waiting on.
//
// It is deliberately NOT generous. A process that ignores SIGTERM is not
// doing cleanup, it is refusing to stop, and cancellation has to remain
// something that actually happens.
const DefaultGraceDelay = 3 * time.Second

// ApplyGracefulCancel makes ctx cancellation TERMINATE cmd's process group
// rather than SIGKILL its direct child, and escalates to a kill if the group
// has not exited within grace.
//
// Why this exists: os/exec's default Cancel is Process.Kill() — SIGKILL. A
// SIGKILLed process runs no signal handler, so every cleanup it would have
// done is lost. Git is the case where that loss is visible and lasting: it
// removes .git/index.lock from an atexit handler, so a SIGKILL strands the
// lock and every later write in that repository fails with a message blaming
// a process that is no longer running. Other tools lose their cleanup just as
// completely, only silently.
//
// # Why this does not just lower WaitDelay
//
// cmd.WaitDelay is ONE timer serving TWO unrelated purposes: the wait for a
// cancelled child to exit (after which os/exec kills it), and the drain of
// output pipes after exit. Those want opposite values — the grace period
// should be short so a cancel is prompt, the drain should be forgiving so a
// grandchild holding the pipe does not truncate real output. Setting
// cmd.Cancel here and keeping the escalation on our OWN timer lets each
// number mean exactly one thing: grace is grace, and WaitDelay stays the
// drain bound it is documented as.
//
// # Process groups
//
// SIGTERM to the direct child alone is not enough. Both exec paths run
// `bash -c <command>`, and bash forks for anything beyond a single simple
// command, so the process doing the work is frequently a GRANDCHILD that
// never sees a signal sent to its parent. Measured: a script whose cleanup
// trap runs in a subshell wrote its marker under group signalling and wrote
// nothing under direct-child signalling. So the whole group is signalled, and
// callers must start the child with its own process group (Setpgid on Unix,
// CREATE_NEW_PROCESS_GROUP on Windows) — both exec paths already do.
//
// # Two ordering hazards this handles
//
// Cancel runs BEFORE Wait reaps the child, so the pid is still un-reaped and
// group-signalling it cannot reach a recycled pid. The escalation timer has
// no such guarantee: it can fire after Wait has reaped. It therefore uses
// Process.Kill(), which os.Process tracks as done and turns into a harmless
// ErrProcessDone, rather than a raw kill on a pid number that may by then
// belong to someone else.
//
// # Reporting
//
// Cancel returns ErrProcessDone when the child is already gone. os/exec
// treats a Cancel error wrapping os.ErrProcessDone as "there was nothing to
// cancel" and lets a successful Wait stay successful — without it, a command
// that finished cleanly in the instant the context was cancelled reports a
// failure that never happened. Verified: Cancel returning nil yields
// waitErr="context canceled" for a child that exited 0; returning
// ErrProcessDone yields waitErr=nil.
//
// cmd MUST come from exec.CommandContext. os/exec rejects Start on a command
// that has a non-nil Cancel but no context ("command with a non-nil Cancel was
// not created with CommandContext"), which is a start-time failure rather than
// a compile-time one — so a caller that switches to exec.Command finds out at
// runtime.
//
// stop must be called after Wait returns, so the escalation timer cannot
// outlive the command; the returned func is safe to call more than once and
// is intended for defer.
func ApplyGracefulCancel(cmd *exec.Cmd, grace time.Duration) (stop func()) {
	if grace <= 0 {
		grace = DefaultGraceDelay
	}

	done := make(chan struct{})
	var stopOnce sync.Once

	cmd.Cancel = func() error {
		proc := cmd.Process
		if proc == nil {
			// Never started, or already cleaned up: nothing to signal, and
			// saying so keeps a clean result clean.
			return os.ErrProcessDone
		}

		err := terminateGracefully(proc)

		// Escalate on our own clock so WaitDelay keeps meaning only the pipe
		// drain. Kill() (not a raw group kill) because this fires
		// asynchronously and may land after Wait has reaped the child.
		timer := time.AfterFunc(grace, func() {
			_ = proc.Kill()
		})
		go func() {
			<-done
			timer.Stop()
		}()

		return err
	}

	// sync.Once rather than a bool: stop is documented as safe to call more
	// than once, and the exec paths call it from whichever goroutine finishes
	// the command, so an unsynchronized flag would be a data race and a
	// double close would panic.
	return func() { stopOnce.Do(func() { close(done) }) }
}
