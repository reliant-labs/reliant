// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ExecDeadline is the foreground lifetime of one command: the thing that
// decides when a command started by exec.run or LocalClient.RunCommand must be
// stopped. It lives here, next to ExecWaitDelay and ExecGraceDelay, because
// both exec paths use it and must not drift.
//
// It replaces `context.WithTimeout(ctx, TimeoutMs)` as the command's context,
// for two reasons that are each a bug that context had.
//
// 1. The timeout must hold on the WALL clock. Go's timers run on the monotonic
// clock, which does not advance while the machine is asleep — on macOS it is
// mach_absolute_time, and Linux's CLOCK_MONOTONIC excludes suspend too. A
// command started with a 60s timeout on a laptop that then slept for two hours
// therefore did not time out on wake: it carried on for whatever was left of
// its 60 seconds of AWAKE time. Measured on the dev stack's tool_calls rows over
// 14 days: every foreground shell call that overran its own timeout while its
// agent was waiting spanned a sleep (`pmset -g log`), and its awake time was
// within its timeout — the timeout was enforced, on a clock the caller does not
// live on. The caller's contract is wall-clock ("return within N ms"), and in
// distributed mode the caller is a worker on a machine that did not sleep and
// has already moved on, so a command that resumes on wake runs to no one,
// mutating a worktree its agent has stopped watching. So the deadline is also
// checked against the wall clock, and a command found past it — which in
// practice means "the machine just woke up" — is stopped then.
//
// The monotonic timer stays: it is precise while the machine is awake, and it
// is immune to the wall clock being set backwards. The command stops at
// whichever deadline comes first.
//
// 2. A command adopted into the background manager must outlive it. The
// command's context used to be cancelled by a deferred cancel() when the exec
// function returned — which it does immediately on adoption — and os/exec
// answers a cancelled context with cmd.Cancel, the graceful SIGTERM to the
// process group. So "push to background" reported a process id and killed the
// process behind it. Release ends foreground supervision WITHOUT stopping the
// command; nothing but the deadline itself ever cancels the context.
type ExecDeadline struct {
	ctx    context.Context
	cancel context.CancelCauseFunc

	released chan struct{}
	once     sync.Once
}

// ErrWallClockDeadline is the reason a command was stopped when its timeout
// had elapsed on the wall clock but not yet on the monotonic one: the machine
// slept through it (or its clock jumped forward). It wraps
// context.DeadlineExceeded, so everything that recognises a timeout recognises
// this one.
var ErrWallClockDeadline = fmt.Errorf("wall-clock %w", context.DeadlineExceeded)

// WallClockTimeoutMessage explains a wall-clock timeout in the one channel
// every consumer reads. The command's own runtime looks far shorter than its
// timeout, so without this a reader sees a command "time out" early for no
// reason.
const WallClockTimeoutMessage = "command timed out: its timeout elapsed while this machine was asleep (or its clock jumped forward), so it was stopped when the machine resumed. It ran for only part of its timeout; re-run it if the result is still needed."

// ExecWallClockCheckInterval is how often a running command compares the wall
// clock against its deadline. It bounds how late after a wake a stale command
// is stopped, and costs one wakeup per interval per RUNNING command — of which
// there are a handful.
//
// A var rather than a const only so tests can shorten it; production never
// reassigns it.
var ExecWallClockCheckInterval = time.Second

// ExecWallClock reads the wall clock. Round(0) strips the monotonic reading so
// comparisons are made on wall time — comparing two time.Now() values directly
// uses the monotonic clock, which is the clock that stopped.
//
// A var only so tests can simulate a machine that slept; production never
// reassigns it.
var ExecWallClock = func() time.Time { return time.Now().Round(0) }

// StartExecDeadline arms the deadline for one command. The command must be
// built with Context(), and Release must be called once the command has
// finished or been adopted. A non-positive timeout means the command is bound
// only by the caller's context.
//
// The command's context carries parent's values but not its cancellation:
// parent being cancelled stops the command through the watcher, exactly like a
// timeout, which is what lets Release detach the command from both.
func StartExecDeadline(parent context.Context, timeout time.Duration) *ExecDeadline {
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(parent))
	d := &ExecDeadline{ctx: ctx, cancel: cancel, released: make(chan struct{})}
	// The package knobs are read here, on the caller's goroutine, so the
	// watcher never reads a var a test may be restoring.
	wallNow, interval := ExecWallClock, ExecWallClockCheckInterval
	go d.watch(parent, timeout, wallNow, wallNow().Add(timeout), interval)
	return d
}

// Context is what the command runs under: done when it must be stopped.
func (d *ExecDeadline) Context() context.Context { return d.ctx }

// Err reports why the command was stopped, nil if it was not:
// context.DeadlineExceeded for a timeout on the monotonic clock,
// ErrWallClockDeadline for one on the wall clock, otherwise the caller's own
// context error. Read it in place of the exec context's Err().
func (d *ExecDeadline) Err() error { return context.Cause(d.ctx) }

// Release ends foreground supervision without stopping the command. Call it
// when the command has finished, and when it is adopted into the background
// manager, from which point it belongs to shell_output / shell_kill and
// neither the foreground timeout nor the caller's context may stop it.
// Safe to call more than once.
func (d *ExecDeadline) Release() {
	d.once.Do(func() { close(d.released) })
}

func (d *ExecDeadline) watch(parent context.Context, timeout time.Duration, wallNow func() time.Time, wallDeadline time.Time, interval time.Duration) {
	// Both nil (never ready) when there is no timeout: the command is then
	// bound only by the caller's context.
	var monotonic, wallCheck <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		monotonic = timer.C

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		wallCheck = ticker.C
	}

	for {
		select {
		case <-d.released:
			return
		case <-parent.Done():
			d.stop(parent.Err())
			return
		case <-monotonic:
			d.stop(context.DeadlineExceeded)
			return
		case <-wallCheck:
			if !wallNow().Before(wallDeadline) {
				d.stop(ErrWallClockDeadline)
				return
			}
		}
	}
}

// stop cancels the command unless it was released in the meantime: select
// picks among ready cases at random, so a deadline that fires in the same
// instant as an adoption must not take down the process just handed off.
func (d *ExecDeadline) stop(cause error) {
	select {
	case <-d.released:
		return
	default:
	}
	d.cancel(cause)
}
