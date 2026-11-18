// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/forkwatch"
	"github.com/reliant-labs/reliant/internal/logging"
)

// The fork watchdog's self-heal, the daemon's half (internal/forkwatch;
// owner-approved 2026-11-18 on reliant#1919). The watchdog decides WHEN; this
// decides WHAT:
//
//   - Heal, after the machine has been unable to start a process for
//     forkwatch.DefaultHealAfter: stop the largest background command this
//     daemon started (BackgroundManager.StopLargest), sized by its process
//     group in a fresh /proc count. Its output says why. Never a foreground
//     command an agent is waiting on, a recovered one, or a package command a
//     person started. On every machine: it is the daemon's own work.
//   - GiveUp, when a start is still refused one probe after that: a managed
//     daemon exits non-zero, like a lost workspace volume (workspace_guard.go).
//     It is the container's main process under RestartPolicy Always, so the
//     restart ends every process in the container and frees the table, and
//     chats carry on once it re-attaches. A person's own daemon does not exit:
//     Electron would respawn it into the same full table, and the processes
//     filling it are the person's to judge, so the degraded reason already
//     tells them to stop what is not needed or restart the machine.

// ProcessTableFullError is what Start returns when a managed daemon stops
// because its machine still could not start processes after the self-heal. Its
// exit is the remedy, not a crash.
type ProcessTableFullError struct {
	Reason string
}

func (e *ProcessTableFullError) Error() string {
	return "this machine's process table stayed full after the self-heal — exiting so the container restarts and frees it: " + e.Reason
}

const (
	// forkHealFlushDelay is how long cancelled in-flight work gets to send
	// its replies before the managed daemon starts shutting down.
	forkHealFlushDelay = 2 * time.Second
	// forkHealExitBackstop bounds the graceful shutdown that follows: on a
	// machine that cannot start processes, a shutdown step that needs one may
	// hang, and a daemon wedged in shutdown is not restarted.
	forkHealExitBackstop = 20 * time.Second
)

// forkHealWhy is what a command the heal stopped says in its output.
var forkHealWhy = fmt.Sprintf("this machine could not start any new process for %s (its process table was full, so every agent's commands were failing), "+
	"and this was the largest background command on it, so the daemon stopped it to free the table", forkwatch.DefaultHealAfter)

// forkHealer carries out the watchdog's remedies. Every method is nil-safe.
type forkHealer struct {
	managed bool
	// recount refreshes the process count, so the heal sizes commands as
	// they are now (procguard.Guard.Check; reads /proc, forks nothing).
	recount func() bool
	// tasks sizes a process group (procguard.Guard.GroupTasks).
	tasks func(pgid int) uint64
	// stopLargest stops the largest background command and describes it
	// (shell.BackgroundManager.StopLargest).
	stopLargest func(tasks func(pgid int) uint64, why string) string

	// exit ends the process when shutdown wedges; terminationLog is where
	// kubelet reads the reason.
	exit           func(code int)
	flushDelay     time.Duration
	exitBackstop   time.Duration
	terminationLog string

	mu             sync.Mutex
	cancelInFlight func()
	stopRuntime    func()
	gaveUpOnce     sync.Once
	gaveUpReason   string
}

// arm hands the healer what stops the runtime, once Start has it.
func (h *forkHealer) arm(cancelInFlight, stopRuntime func()) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancelInFlight, h.stopRuntime = cancelInFlight, stopRuntime
}

// heal is forkwatch.Config.Heal.
func (h *forkHealer) heal(ctx context.Context) string {
	if h == nil || h.stopLargest == nil {
		return ""
	}
	if h.recount != nil {
		h.recount()
	}
	stopped := h.stopLargest(h.tasks, forkHealWhy)
	logging.WarnContext(ctx, logPrefix+" Machine cannot start processes; self-heal stopped the largest background command",
		"stopped", stopped)
	return stopped
}

// giveUp is forkwatch.Config.GiveUp, on a managed daemon only: it stops the
// daemon, once, so the container restarts.
func (h *forkHealer) giveUp(reason string) {
	if h == nil || !h.managed {
		return
	}
	h.gaveUpOnce.Do(func() {
		h.mu.Lock()
		h.gaveUpReason = reason
		cancelInFlight, stopRuntime := h.cancelInFlight, h.stopRuntime
		h.mu.Unlock()

		writeTerminationMessage(h.terminationLog, "This machine could not start processes (process table full), and stopping its largest background command did not free it: "+
			"the daemon exited so the container restarts.\n"+reason)
		if cancelInFlight != nil {
			cancelInFlight()
		}
		time.AfterFunc(h.flushDelay, func() {
			logging.Error(logPrefix + " Stopping the daemon so the container restarts and frees its process table")
			if stopRuntime != nil {
				stopRuntime()
			}
		})
		time.AfterFunc(h.flushDelay+h.exitBackstop, func() {
			logging.Error(logPrefix+" Shutdown for a full process table did not finish — force-exiting", "backstop", h.exitBackstop)
			if h.exit != nil {
				h.exit(1)
			}
		})
	})
}

// gaveUp is non-nil once the managed daemon has begun exiting for a full
// process table. Start returns it in place of the cancellation it caused.
func (h *forkHealer) gaveUp() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gaveUpReason == "" {
		return nil
	}
	return &ProcessTableFullError{Reason: h.gaveUpReason}
}
