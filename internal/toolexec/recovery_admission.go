// Copyright (c) 2026 Reliant Labs
package toolexec

import (
	"errors"
	"strings"

	"github.com/reliant-labs/reliant/internal/observability"
)

// ErrRecoveryFinalizing is a daemon's refusal of new work in a worktree while
// it finalizes that worktree's recovery (a remove or reclone: the short
// recheck/quarantine/removal window daemonruntime.AcquireWorktreeRecovery
// leases). Nothing ran, and the caller retries; the web, the agent and the
// workspace-create workflow already do.
//
// The daemon returns this error itself, and its text crosses the wire as a
// daemon command's failure text: the message is the wire contract, and every
// daemon since #2549 writes exactly it. So it is never reworded.
var ErrRecoveryFinalizing = errors.New("workspace recovery finalization in progress; retry this operation")

// ErrorCodeRecoveryFinalizing marks a tool result the daemon refused with
// ErrRecoveryFinalizing: the tool did not run, and the model reads the
// refusal as an ordinary tool error.
const ErrorCodeRecoveryFinalizing = "RECOVERY_FINALIZING"

// IsRecoveryFinalizing reports whether err is, or wraps, a daemon's
// ErrRecoveryFinalizing refusal: the daemon's own error, or the router's
// error for a daemon command the daemon answered with it.
func IsRecoveryFinalizing(err error) bool {
	return errors.Is(err, ErrRecoveryFinalizing)
}

// isRecoveryRefusalText reports whether a daemon's failure text is its
// ErrRecoveryFinalizing refusal. A handler may have wrapped it, so the text is
// matched anywhere in the message.
func isRecoveryRefusalText(text string) bool {
	return strings.Contains(text, ErrRecoveryFinalizing.Error())
}

// Labels of DaemonRecoveryAdmissionRefusedTotal: what the daemon refused.
const (
	recoveryRefusedCommand  = "command"
	recoveryRefusedToolCall = "tool_call"
)

// noteRecoveryRefusal counts one refusal the daemon answered with
// ErrRecoveryFinalizing. It is logged below ERROR wherever it surfaces (it is
// the system working, and the caller retries), so this counter is how a
// refusal that does not clear stays visible.
func noteRecoveryRefusal(kind string) {
	observability.DaemonRecoveryAdmissionRefusedTotal.WithLabelValues(kind).Inc()
}
