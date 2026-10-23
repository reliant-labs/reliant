package debug

import (
	"context"
	"fmt"
	"os"
	"os/exec"

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

For an ATTACH session (forge debug --attach <pid>), stop DETACHES the
debugger and leaves the target process running. forge never kills a process
it did not launch.`,
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
	// actionDetach: leave the target running, just drop the debugger
	// connection. The mandatory choice for ATTACH sessions.
	actionDetach stopAction = iota
	// actionKillTarget: terminate the target (forge launched it).
	actionKillTarget
	// actionStopDocker: tear down the docker debug container.
	actionStopDocker
)

// stopActionFor decides what happens to the target. The cardinal rule: only
// an OWNED session's target may be killed. A nil session, or any session
// where Owned is false (an attach), MUST detach — forge never kills a
// process it did not launch.
func stopActionFor(s *dbgsvc.SessionInfo) stopAction {
	switch {
	case s != nil && s.Docker:
		return actionStopDocker
	case owned(s):
		return actionKillTarget
	default:
		return actionDetach
	}
}

// runStop tears down a debug session safely. The cardinal rule: only kill
// processes forge launched (session.Owned). An attach session is detached,
// never killed — terminating an attached live process is a data-loss-class
// bug.
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
		// Live RPC connection: detach cleanly via the right method.
		switch action {
		case actionStopDocker:
			dbg.Disconnect()
			stopDockerDebug(ctx)
		case actionKillTarget:
			if err := dbg.Stop(); err != nil { // Stop detaches+kills the debuggee.
				fmt.Fprintf(os.Stderr, "Warning: error stopping debugger: %v\n", err)
			}
		default: // actionDetach
			dbg.Disconnect() // detach WITHOUT killing — target must survive.
		}
	} else {
		// Couldn't connect (dlv busy / already gone). Fall back to process
		// signals — but STILL respect the decided action.
		switch action {
		case actionStopDocker:
			stopDockerDebug(ctx)
		case actionKillTarget:
			if session != nil && session.PID > 0 {
				if p, findErr := os.FindProcess(session.PID); findErr == nil {
					_ = p.Kill()
				}
			}
		default: // actionDetach: NEVER kill the target.
		}
	}

	// Reap the dlv server process forge spawned (orphan dlv attach/exec
	// otherwise survive stop and hold the target halted / the port open).
	reapDlv(ctx, session)

	if err := debugSvc().ClearSession("."); err != nil {
		return fmt.Errorf("clearing session: %w", err)
	}

	switch action {
	case actionStopDocker:
		fmt.Println("Docker debug session stopped.")
	case actionKillTarget:
		fmt.Println("Debug session stopped (debugged process killed).")
	default:
		fmt.Println("Debug session stopped (detached; target process left running).")
	}
	return nil
}

// owned reports whether forge launched the session's target process. A nil
// session is treated as NOT owned — the safe default is to never kill.
func owned(s *dbgsvc.SessionInfo) bool { return s != nil && s.Owned }

func stopDockerDebug(ctx context.Context) {
	stopCmd := exec.CommandContext(ctx, "docker", "compose", "--profile", "debug", "stop", "app-debug")
	_ = stopCmd.Run()
}

// reapDlv kills the dlv server process forge spawned for this session and
// sweeps any stray dlv server bound to the session's listen address. dlv
// holds the target halted while attached; reaping it both frees the port and
// lets an attached target resume normal execution.
func reapDlv(ctx context.Context, session *dbgsvc.SessionInfo) {
	if session == nil {
		return
	}
	// dlv is the debuggee's parent, so it is in this command's lineage
	// whenever the debuggee is: never reap a process hosting this command.
	self := procguard.Self()
	// 1. The dlv PID forge recorded at start.
	// Only killed if it is still a dlv on this session's addr: the pid may
	// have been recycled. If it does not verify, the sweep below still finds
	// the real dlv.
	if session.DlvPID > 0 && !self.Contains(session.DlvPID) && isDlvForAddr(ctx, session.DlvPID, session.Addr) {
		if p, err := os.FindProcess(session.DlvPID); err == nil {
			_ = p.Kill()
		}
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
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
}
