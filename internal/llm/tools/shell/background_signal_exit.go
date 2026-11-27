// Copyright (c) 2026 Reliant Labs
package shell

import (
	"fmt"
	"os/exec"
	"syscall"
)

// shellSignalExits are the signals a shell's 128+N exit status is read as:
// the ones something sends to stop a command (hangup, interrupt, kill,
// terminate). Another N in that range is as likely a command's own exit code.
var shellSignalExits = map[syscall.Signal]bool{
	syscall.SIGHUP:  true,
	syscall.SIGINT:  true,
	syscall.SIGKILL: true,
	syscall.SIGTERM: true,
}

// signalExitNote explains, in the command's output, a background command that
// a signal this daemon did not send ended; "" for any other exit, and for one
// ours ended (stoppedByUs: KillProcess, the idle reaper, a shutdown).
//
// The command's leader was either killed by the signal itself (Go reports exit
// code -1) or was a shell or the heavy-slot wrapper that reported its child's
// death the shell way, 128+N. Without the note the record reads "failed, exit
// code -1" with output that stops mid-run, and nothing says the command did not
// fail on its own and needs rerunning (reliant#2751).
func signalExitNote(exitErr *exec.ExitError, stoppedByUs bool) string {
	if exitErr == nil || stoppedByUs {
		return ""
	}
	sig, viaShell, ok := exitSignal(exitErr)
	if !ok {
		return ""
	}
	how := fmt.Sprintf("was ended by signal %d (%s)", int(sig), sig)
	if viaShell {
		how = fmt.Sprintf("exited with code %d, which is how a shell reports a command ended by signal %d (%s)",
			exitErr.ExitCode(), int(sig), sig)
	}
	return "[reliant] This background process " + how + ". Reliant did not stop it (when it does, it says so here): " +
		"something else on the machine sent the signal, such as a kill or pkill run by another agent or command, or the system. " +
		"Its work did not finish; re-run it if you still need its result."
}

// exitSignal reads the signal that ended a process from its exit: the wait
// status's own signal, or a shell's 128+N for one of shellSignalExits.
func exitSignal(exitErr *exec.ExitError) (sig syscall.Signal, viaShell, ok bool) {
	if ws, isWS := exitErr.Sys().(syscall.WaitStatus); isWS && ws.Signaled() {
		return ws.Signal(), false, true
	}
	if code := exitErr.ExitCode(); code > 128 {
		if s := syscall.Signal(code - 128); shellSignalExits[s] {
			return s, true, true
		}
	}
	return 0, false, false
}
