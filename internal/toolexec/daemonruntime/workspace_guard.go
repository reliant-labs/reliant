// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon/rootwatch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The workspace guard is the daemon's policy on top of rootwatch: what to do
// when $HOME, or a project/worktree root the daemon serves, stops being the
// directory it was. See rootwatch's package comment for the 2026-10-09
// incident this exists for, and for how a lost mount is told apart from a
// worktree being deleted.
//
// Two answers, by who can fix it:
//
//   - A managed workspace (the operator's pod, RELIANT_DAEMON_TYPE=managed)
//     that loses $HOME has lost its volume. The daemon is the container's
//     main process under RestartPolicy Always, and a container restart
//     re-attaches the volume — nothing else can. So it refuses new work,
//     cancels in-flight work, gives the replies a moment to reach the
//     gateway, and exits non-zero. The server tolerates a daemon being
//     unreachable for two minutes before pausing a chat
//     (runtime.DaemonOfflinePauseGrace), and the restarted daemon re-asserts
//     the same identity, so chats carry on where they were.
//   - Everything else — a desktop daemon, or a root other than $HOME — has no
//     supervisor that could fix it: Electron would merely respawn the daemon
//     onto whatever is at the path now. So the daemon keeps running, says so
//     loudly, and refuses work under the affected path until the original
//     directory is back or the user restarts it to adopt what is there now.
//     Refusing matters: an agent that keeps working writes onto a disk nobody
//     meant (on 2026-10-09, ~/.config, ~/.docker and a fake worktree landed on
//     a container rootfs that the next restart discarded).
//
// The error a refused tool call returns names the cause. Without this, every
// call failed with "working directory does not exist", which sent agents and
// the user hunting for a deleted worktree that was never deleted.
const (
	errorCodeWorkspaceVolumeLost  = "WORKSPACE_VOLUME_LOST"
	errorCodeWorkspaceRootChanged = "WORKSPACE_ROOT_CHANGED"

	// volumeLostFlushDelay is how long cancelled in-flight work gets to send
	// its replies before the managed daemon starts shutting down.
	volumeLostFlushDelay = 2 * time.Second
	// volumeLostExitBackstop bounds the graceful shutdown that follows. The
	// same reasoning as daemonShutdownGrace: a path that can hang forever is
	// what turns "exit so kubelet restarts us" into another dead daemon.
	volumeLostExitBackstop = 20 * time.Second
	// terminationMessagePath is kubelet's default terminationMessagePath. What
	// is written there shows up in `kubectl describe pod` as the last
	// termination's message, next to the exit code.
	terminationMessagePath = "/dev/termination-log"
	// terminationMessageLimit is kubelet's per-container cap.
	terminationMessageLimit = 4096
)

// Seams for the end-to-end test of Start, which must neither wait out the real
// poll interval nor be killed by the exit backstop, which deliberately stays
// armed after Start returns: it bounds the CLI's own teardown too.
var (
	// workspaceGuardPollInterval is how often the guard re-checks everything
	// it watches between tool calls.
	workspaceGuardPollInterval = rootwatch.DefaultInterval
	// workspaceGuardExit ends the process when shutdown wedges.
	workspaceGuardExit = os.Exit
)

// WorkspaceVolumeLostError is what Start returns when a managed daemon stops
// because its workspace volume went away. Its exit is the remedy, not a crash.
type WorkspaceVolumeLostError struct {
	Fault rootwatch.Fault
}

func (e *WorkspaceVolumeLostError) Error() string {
	return fmt.Sprintf("workspace volume lost (%v) — exiting so the container restarts and re-attaches it", e.Fault)
}

func (e *WorkspaceVolumeLostError) Unwrap() error { return e.Fault }

// restartHeals reports whether exiting is the remedy for f: only on a managed
// workspace, and only for $HOME — the volume the pod mounts, which a container
// restart re-attaches. A restart cannot bring back anything else.
func restartHeals(f rootwatch.Fault, managed bool) bool {
	return managed && f.Home
}

// faultMessage is what a refused tool call or command tells the agent and the
// user, and the error code it carries.
func faultMessage(f rootwatch.Fault, managed bool) (message, code string) {
	switch {
	case restartHeals(f, managed):
		return fmt.Sprintf("The workspace volume was lost: %s is no longer the disk this machine started with, "+
			"so the files on it cannot be seen right now. They have not been deleted. "+
			"The machine is restarting to re-attach the volume and will reconnect by itself, usually within a minute. "+
			"Retry this step once it is back.", f.Path), errorCodeWorkspaceVolumeLost
	case f.Home:
		return fmt.Sprintf("Your home directory %s changed while the Reliant daemon was running: it no longer resolves "+
			"to the directory the daemon started with, because the disk or mount behind it was unmounted or replaced. "+
			"Restore the mount, or restart the Reliant daemon to work with what is there now.", f.Path), errorCodeWorkspaceRootChanged
	default:
		return fmt.Sprintf("The filesystem holding %s changed while the Reliant daemon was running: the disk or mount "+
			"behind it was unmounted or replaced, so this path no longer shows the files the daemon was working with. "+
			"Remount it, or restart the Reliant daemon to work with what is at that path now.", f.Path), errorCodeWorkspaceRootChanged
	}
}

// workspaceGuard owns the watcher and acts on its faults. Every method is
// nil-safe: a daemon without one (tests, Windows) simply is not guarded.
type workspaceGuard struct {
	watcher *rootwatch.Watcher
	managed bool

	// Set before the watcher's first check; tests replace them.
	cancelInFlight func()
	stopRuntime    func()
	exit           func(code int)
	announce       func(line string)
	flushDelay     time.Duration
	exitBackstop   time.Duration
	terminationLog string

	restartOnce sync.Once
	mu          sync.Mutex
	lost        *rootwatch.Fault
}

// newWorkspaceGuard records home and returns a guard over it. err explains
// why home is not watched; the guard is still usable for roots.
func newWorkspaceGuard(home string, managed bool, opts rootwatch.Options) (*workspaceGuard, error) {
	g := &workspaceGuard{
		managed:        managed,
		exit:           workspaceGuardExit,
		flushDelay:     volumeLostFlushDelay,
		exitBackstop:   volumeLostExitBackstop,
		terminationLog: terminationMessagePath,
	}
	opts.OnFault = g.handleFault
	opts.OnRestored = g.handleRestored
	watcher, err := rootwatch.New(home, opts)
	g.watcher = watcher
	return g, err
}

// armWorkspaceGuard records $HOME and starts watching it; project and
// worktree roots are recorded as tool calls first name them. stopRuntime ends
// Start's run loop — it is how a managed daemon exits for a lost volume.
func (d *daemonClient) armWorkspaceGuard(ctx context.Context, stopRuntime func()) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	guard, recordErr := newWorkspaceGuard(home, IsManagedEnvironment(), rootwatch.Options{Interval: workspaceGuardPollInterval})
	guard.cancelInFlight = d.cancelAllRequests
	guard.stopRuntime = stopRuntime
	if !d.bootCfg.Verbose {
		// Verbose mode already has the ERROR line on stdout; a person running
		// `reliant daemon start` in a terminal otherwise sees only the file log.
		guard.announce = func(line string) { fmt.Println(line) }
	}
	d.guard = guard

	guard.logStartup(recordErr)
	go guard.run(ctx)
}

// run polls until ctx is done.
func (g *workspaceGuard) run(ctx context.Context) {
	if g == nil {
		return
	}
	g.watcher.Run(ctx)
}

// observe records a root the daemon serves, the first time it is seen.
func (g *workspaceGuard) observe(root string) {
	if g == nil {
		return
	}
	g.watcher.Observe(root)
}

// preflight re-checks $HOME and root now, and returns the refusal for a tool
// call under root, or nil to let it run.
func (g *workspaceGuard) preflight(root string) *toolexec.ExecutionResult {
	if g == nil {
		return nil
	}
	if f, ok := g.watcher.Check(root); ok {
		return g.refusal(f)
	}
	return nil
}

// verdict is the refusal for a tool call under root that has already run,
// from what the checks have found; it touches no filesystem. A call that was
// in flight when its directory went away reports that, not whatever its
// cancellation or its missing working directory made of it.
func (g *workspaceGuard) verdict(root string) *toolexec.ExecutionResult {
	if g == nil {
		return nil
	}
	if f, ok := g.watcher.Fault(root); ok {
		return g.refusal(f)
	}
	return nil
}

// commandPreflight is preflight for daemon commands, whose payloads are not
// parsed for a path: only a fault covering everything ($HOME) refuses them.
func (g *workspaceGuard) commandPreflight() error {
	if g == nil {
		return nil
	}
	if f, ok := g.watcher.Check(""); ok {
		return g.refusalErr(f)
	}
	return nil
}

// commandVerdict is verdict for daemon commands.
func (g *workspaceGuard) commandVerdict() error {
	if g == nil {
		return nil
	}
	if f, ok := g.watcher.Fault(""); ok {
		return g.refusalErr(f)
	}
	return nil
}

// lostVolume is non-nil once the managed daemon has begun exiting for a lost
// workspace volume. Start returns it in place of the cancellation it caused.
func (g *workspaceGuard) lostVolume() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lost == nil {
		return nil
	}
	return &WorkspaceVolumeLostError{Fault: *g.lost}
}

func (g *workspaceGuard) refusal(f rootwatch.Fault) *toolexec.ExecutionResult {
	msg, code := faultMessage(f, g.managed)
	return &toolexec.ExecutionResult{
		Success:      false,
		IsError:      true,
		Content:      msg,
		ErrorMessage: f.Error(),
		ErrorCode:    code,
	}
}

func (g *workspaceGuard) refusalErr(f rootwatch.Fault) error {
	msg, _ := faultMessage(f, g.managed)
	return errors.New(msg)
}

// logStartup records what is being watched, so a later fault has a baseline
// in the same log.
func (g *workspaceGuard) logStartup(recordErr error) {
	if g == nil {
		return
	}
	rec, ok := g.watcher.Home()
	if !ok {
		logging.Warn(logPrefix+" Workspace-root watchdog is not watching $HOME", "error", recordErr)
		return
	}
	logging.Info(logPrefix+" Workspace-root watchdog armed",
		"home", rec.Path, "dev", rec.Identity.Dev, "ino", rec.Identity.Ino,
		"mount_point", rec.MountPoint, "managed", g.managed,
		"mounts", strings.Join(rec.Mounts, " | "))
	if g.managed && !rec.MountPoint {
		// The operator mounts the workspace PVC at $HOME. If it is not a
		// mount point now, the daemon started without its volume and
		// everything written is going to the container's own disk.
		logging.Warn(logPrefix+" $HOME is not a mount point on a managed workspace — the workspace volume may not be attached; files written now are lost on restart",
			"home", rec.Path)
	}
}

func (g *workspaceGuard) handleFault(f rootwatch.Fault) {
	restart := restartHeals(f, g.managed)
	action := "refusing work under this path until it is restored or the daemon is restarted"
	if restart {
		action = "exiting so the container restarts and re-attaches the volume"
	}
	logging.Error(logPrefix+" Workspace directory changed under the running daemon",
		"path", f.Path,
		"home", f.Home,
		"kind", string(f.Kind),
		"recorded_dev", f.Recorded.Dev,
		"recorded_ino", f.Recorded.Ino,
		"observed_dev", f.Observed.Dev,
		"observed_ino", f.Observed.Ino,
		"mounts_at_start", strings.Join(f.RecordedMounts, " | "),
		"mounts_now", strings.Join(f.Mounts, " | "),
		"managed", g.managed,
		"pid", os.Getpid(),
		"action", action,
		"error", f,
	)
	msg, _ := faultMessage(f, g.managed)
	if g.announce != nil {
		g.announce("  ! " + msg)
	}
	if restart {
		g.beginRestart(f, msg)
	}
}

func (g *workspaceGuard) handleRestored(f rootwatch.Fault) {
	logging.Info(logPrefix+" Workspace directory restored; accepting work under it again",
		"path", f.Path, "dev", f.Recorded.Dev, "ino", f.Recorded.Ino)
}

// beginRestart stops the managed daemon, once.
func (g *workspaceGuard) beginRestart(f rootwatch.Fault, msg string) {
	g.restartOnce.Do(func() {
		g.mu.Lock()
		g.lost = &f
		g.mu.Unlock()

		writeTerminationMessage(g.terminationLog, msg+"\n"+f.Error())
		if g.cancelInFlight != nil {
			g.cancelInFlight()
		}
		time.AfterFunc(g.flushDelay, func() {
			logging.Error(logPrefix+" Stopping the daemon so the container restarts and re-attaches the workspace volume",
				"path", f.Path)
			if g.stopRuntime != nil {
				g.stopRuntime()
			}
		})
		time.AfterFunc(g.flushDelay+g.exitBackstop, func() {
			logging.Error(logPrefix+" Shutdown after losing the workspace volume did not finish — force-exiting",
				"path", f.Path, "backstop", g.exitBackstop)
			g.exit(1)
		})
	})
}

// writeTerminationMessage puts msg where kubelet reads a container's last
// words. It never creates the file: off Kubernetes there is nothing there,
// and nothing should be.
func writeTerminationMessage(path, msg string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	if len(msg) > terminationMessageLimit {
		msg = msg[:terminationMessageLimit]
	}
	_, _ = f.WriteString(msg)
}
