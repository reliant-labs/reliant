package debug

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	dbgsvc "github.com/reliant-labs/forge/internal/debug"
	"github.com/reliant-labs/forge/internal/procguard"
)

func newStopCmd(_ *factory.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the debug session (detaches from attached processes; kills only what forge launched)",
		Long: `Stop the debug session.

For sessions forge launched (forge debug start <service> / --docker), stop
kills the debugged process and reaps the dlv server.

For an ATTACH session (forge debug start --attach <pid>), stop DETACHES the
debugger: Delve removes every breakpoint it inserted and the target keeps
running. forge never kills a process it did not launch.

For a CONNECTED session (forge debug connect <addr>), stop clears the
session's breakpoints, resumes the target and disconnects; the dlv server and
its target keep running.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			session, _ := debugSvc().LoadSession(".")
			return runStop(cmd.Context(), session)
		},
	}
	return cmd
}

// stopAction is the decision about what to do with the debugged TARGET
// process when stopping a session. It is the safety-critical core of stop,
// kept as a pure function (stopActionFor) so the "attach => never kill"
// invariant is unit-testable without a live dlv.
type stopAction int

const (
	// actionDetach: Delve detaches — removing its breakpoints — and the
	// target keeps running. The mandatory choice for ATTACH sessions.
	actionDetach stopAction = iota
	// actionKillTarget: terminate the target (forge launched it).
	actionKillTarget
	// actionStopDocker: tear down the docker debug container.
	actionStopDocker
	// actionRelease: clear breakpoints, resume, disconnect — the dlv server
	// is someone else's and keeps running.
	actionRelease
)

// stopActionFor decides what happens to the target. The cardinal rule: only
// an OWNED session's target may be killed. A nil session, or any session
// where Owned is false (an attach), MUST detach — forge never kills a
// process it did not launch.
func stopActionFor(s *dbgsvc.SessionInfo) stopAction {
	switch {
	case s != nil && s.Docker:
		return actionStopDocker
	case s != nil && s.Connected:
		return actionRelease
	case owned(s):
		return actionKillTarget
	default:
		return actionDetach
	}
}

// runStop tears down a debug session safely. The cardinal rule: only kill
// processes forge launched (session.Owned). An attach session is detached,
// never killed — terminating an attached live process is a data-loss-class
// bug, and so is leaving its breakpoints behind (the next hit kills it with
// SIGTRAP).
func runStop(ctx context.Context, session *dbgsvc.SessionInfo) error {
	action := stopActionFor(session)
	// A debugged process can be the one running this command (a server under
	// `forge debug start` that hosts the shell typing `forge debug stop`).
	// Killing it would end that session, so it is detached instead — the
	// same lineage rule every forge stop path follows (see procguard).
	if action == actionKillTarget && procguard.Self().Contains(session.PID) {
		fmt.Fprintf(os.Stderr, "skipped killing pid %d: it is an ancestor of this command — stopping it would end the session running you; detaching instead\n", session.PID)
		action = actionDetach
	}
	dbg, connErr := connectToSession()

	if connErr == nil {
		switch action {
		case actionStopDocker:
			dbg.Disconnect()
			stopDockerDebug(ctx)
		case actionKillTarget:
			if err := dbg.Stop(); err != nil { // Stop detaches+kills the debuggee.
				// A wedged dlv: fall back to the signal, as when it cannot
				// be reached at all.
				fmt.Fprintf(os.Stderr, "Warning: error stopping debugger: %v; killing pid %d\n", err, session.PID)
				killTarget(session)
			}
		case actionRelease:
			if err := dbg.Release(session.BreakpointIDs); err != nil {
				return fmt.Errorf("releasing the dlv server at %s (session kept; retry 'forge debug stop'): %w", session.Addr, err)
			}
		default: // actionDetach
			// Detach — never just close the connection: Delve must remove
			// the breakpoints it wrote into the live process. If the RPC
			// fails, reapDlv's SIGTERM makes dlv itself detach the same way.
			if err := dbg.Detach(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: detach RPC failed (%v); asking dlv to detach on SIGTERM\n", err)
			}
		}
	} else {
		// Couldn't connect (dlv busy / already gone). Fall back to process
		// signals — but STILL respect the decided action.
		switch action {
		case actionStopDocker:
			stopDockerDebug(ctx)
		case actionKillTarget:
			killTarget(session)
		case actionRelease:
			return fmt.Errorf("cannot reach the dlv server at %s to clear its breakpoints (session kept): %w", session.Addr, connErr)
		default: // actionDetach: NEVER kill the target; reapDlv's SIGTERM detaches.
		}
	}

	// Reap the dlv server process forge spawned (orphan dlv attach/exec
	// otherwise survive stop and hold the target halted / the port open).
	// A connected session's server is not forge's to reap.
	if action != actionRelease {
		reapDlv(ctx, session)
	}

	if err := debugSvc().ClearSession("."); err != nil {
		return fmt.Errorf("clearing session: %w", err)
	}

	switch action {
	case actionStopDocker:
		fmt.Println("Docker debug session stopped.")
	case actionKillTarget:
		fmt.Println("Debug session stopped (debugged process killed).")
	case actionRelease:
		fmt.Println("Debug session stopped (breakpoints cleared, target resumed; the dlv server keeps running).")
	default:
		fmt.Println("Debug session stopped (detached; breakpoints removed, target process left running).")
	}
	return nil
}

// killTarget signals an owned session's target directly: the fallback when
// dlv cannot stop it.
func killTarget(session *dbgsvc.SessionInfo) {
	if session != nil && session.PID > 0 {
		if p, err := os.FindProcess(session.PID); err == nil {
			_ = p.Kill()
		}
	}
}

// owned reports whether forge launched the session's target process. A nil
// session is treated as NOT owned — the safe default is to never kill.
func owned(s *dbgsvc.SessionInfo) bool { return s != nil && s.Owned }

func stopDockerDebug(ctx context.Context) {
	stopCmd := exec.CommandContext(ctx, "docker", composeArgs("--profile", "debug", "stop", "app-debug")...)
	_ = stopCmd.Run()
}

// dlvTermGrace is how long reapDlv waits for dlv to exit on SIGTERM before
// killing it. dlv's SIGTERM handler halts the target and detaches —
// removing its breakpoints from an attached process — which a SIGKILL
// skips. A var so tests can shrink it.
var dlvTermGrace = 5 * time.Second

// reapDlv stops the dlv server process forge spawned for this session and
// sweeps any stray dlv server bound to the session's listen address. Each is
// asked to exit with SIGTERM first (dlv then detaches cleanly) and killed
// only if it does not.
func reapDlv(ctx context.Context, session *dbgsvc.SessionInfo) {
	if session == nil {
		return
	}
	// dlv is the debuggee's parent, so it is in this command's lineage
	// whenever the debuggee is: never reap a process hosting this command.
	self := procguard.Self()
	// 1. The dlv PID forge recorded at start.
	// Only signalled if it is still a dlv on this session's addr: the pid
	// may have been recycled. If it does not verify, the sweep below still
	// finds the real dlv.
	if session.DlvPID > 0 && !self.Contains(session.DlvPID) && isDlvForAddr(ctx, session.DlvPID, session.Addr) {
		terminateDlv(ctx, session, session.DlvPID)
	}
	// 2. Sweep any dlv server still listening on the session addr (covers a
	// dlv whose PID we never captured — e.g. a session reconnected from an
	// older session file, or a docker-discovered server). Match on the
	// --listen=<addr> flag so we only target THIS session's dlv, never an
	// unrelated debugger.
	if session.Addr == "" {
		return
	}
	for _, pid := range dlvPIDsForAddr(ctx, session.Addr) {
		// Never reap the target PID even if it somehow matched.
		if pid == session.PID || self.Contains(pid) {
			continue
		}
		terminateDlv(ctx, session, pid)
	}
}

// terminateDlv sends dlv SIGTERM, waits up to dlvTermGrace for it to exit,
// then kills it.
func terminateDlv(ctx context.Context, session *dbgsvc.SessionInfo, pid int) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if p.Signal(syscall.SIGTERM) == nil {
		deadline := time.Now().Add(dlvTermGrace)
		for time.Now().Before(deadline) {
			if !isDlvForAddr(ctx, pid, session.Addr) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !isDlvForAddr(ctx, pid, session.Addr) {
		return
	}
	_ = p.Kill()
	if !owned(session) {
		fmt.Fprintf(os.Stderr, "Warning: dlv (pid %d) did not exit on SIGTERM and was killed; breakpoints may remain in pid %d\n", pid, session.PID)
	}
}
