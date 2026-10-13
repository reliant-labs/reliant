// Copyright (c) 2025 Reliant Labs

// Package daemonerr classifies the failure of a command the server sent to a
// user's daemon, and maps it onto the Connect error a handler returns.
//
// A daemon command crosses NATS and comes back as an opaque error: the router
// flattens transport failures, timeouts, unresolved daemons, unknown commands
// and daemon-side handler errors into one error whose sentinel (toolexec) or
// text is the only discriminator. Every handler that forwards to a daemon asks
// this package what that failure means before choosing its own code, so the
// machine's state decides the wire code the same way everywhere.
//
//forge:exclude-contract: pure classification of daemon-command failures onto Connect codes; no I/O, state or time
package daemonerr

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Unreachable reports whether err says the request never reached its
// machine because the machine is not connected: resolution found it suspended
// or not yet attached (toolexec.ErrDaemonPending), or NATS had no responder on
// its subject (the "no daemon connected" Unavailable from the router). Either
// way nothing ran, so waking the machine and asking again is safe even for a
// write.
func Unreachable(err error) bool {
	if toolexec.IsDaemonPending(err) {
		return true
	}
	return connect.CodeOf(err) == connect.CodeUnavailable &&
		strings.Contains(err.Error(), "no daemon connected")
}

// dirNotExistMarker mirrors daemonruntime's pkgDirNotExistPrefix. The
// two constants live in different packages (the proxy must not import the daemon
// runtime), and the error text is the wire contract between them: daemon-command
// errors cross the transport as plain strings, not wrapped Go errors.
const dirNotExistMarker = "working dir does not exist"

// outdatedMarker mirrors the phrase daemonruntime's
// unknownCommandError builds. Same wire-contract reasoning as above: the text
// is all that survives the transport.
//
// A daemon reporting this is older than the server asking, which is a state the
// user has to resolve by updating their machine — retrying cannot help.
const outdatedMarker = "older version of Reliant"

// IsOutdated reports whether err is a daemon-too-old failure, i.e.
// the server asked for a command the daemon's build does not have.
func IsOutdated(err error) bool {
	return err != nil && strings.Contains(err.Error(), outdatedMarker)
}

// OutdatedConnectError maps a version-skew failure onto
// FailedPrecondition, preserving the daemon's own explanation.
//
// FailedPrecondition rather than Internal or Unavailable, deliberately:
// Internal says Reliant is broken and invites a bug report for something that
// is not a defect, and Unavailable invites a retry that cannot succeed until
// the daemon is updated. FailedPrecondition is the code that means "the system
// is not in the state this call requires" — which is exactly true, and is the
// only one of the three that points the user at the action that fixes it.
func OutdatedConnectError(err error) *connect.Error {
	return connect.NewError(connect.CodeFailedPrecondition, err)
}

// MachineStateConnectError returns a daemon-command failure that the machine's
// state explains — rather than the command itself — as the Connect error that
// state calls for, wrapped with what the handler was doing (doing may be
// empty). ok is false for any other failure, which the handler maps itself.
//
//   - not reachable right now — starting, asleep, or not connected (NATS had
//     no responder) — is Unavailable: retryable, what the web's daemon-wait
//     machinery expects, and the message keeps the "no daemon connected"
//     marker isDaemonConnectingError keys on;
//   - no machine at all (toolexec.ErrNoDaemon) is FailedPrecondition: waiting
//     cannot produce a machine the user has not connected;
//   - connected but too busy to answer before the deadline
//     (toolexec.ErrDaemonTimeout) is DeadlineExceeded: retryable, and still a
//     server condition — forge logs it at WARN, visible but never a page.
//
// Every handler that forwards to a daemon checks this before choosing its own
// code. The OAuth helper and ListProcesses did not, and answered "no machine
// is connected to your account yet" as Internal (ELECTRON-B1, A6): a 500 for a
// user who simply has no machine. A timeout fell through the same way: one
// saturated cloud machine's file-tree polls answered Internal, logged ERROR,
// and paged as "[gRPC] Request error" (2026-10-10). The wire code is the
// handler's choice, so it is made here rather than read off the error (forge
// svcerr: a cause never decides what the client sees).
func MachineStateConnectError(doing string, err error) (*connect.Error, bool) {
	var code connect.Code
	switch {
	case Unreachable(err):
		code = connect.CodeUnavailable
	case toolexec.IsNoDaemon(err):
		code = connect.CodeFailedPrecondition
	case toolexec.IsDaemonTimeout(err):
		code = connect.CodeDeadlineExceeded
	default:
		return nil, false
	}
	if doing != "" {
		err = fmt.Errorf("%s: %w", doing, err)
	}
	return connect.NewError(code, err), true
}

// DispatchConnectError converts a SendDaemonCommand failure into a Connect
// error whose code reflects the failure class. SendDaemonCommand flattens every
// failure — transport, timeout, unresolved daemon, unknown command, and
// daemon-side handler errors — into a single opaque error whose text is the only
// discriminator, so this classifies by substring (the same approach fs_proxy
// uses for "path is a directory").
//
// The invariant that matters most: whatever the class, the caller gets a loud
// Connect error, NEVER an empty success.
func DispatchConnectError(commandType string, err error) *connect.Error {
	// The daemon is older than this server and has no handler for the command.
	// Surfaced verbatim and WITHOUT the "daemon command X failed" wrapper: the
	// daemon's message already names the command and states the fix, and
	// prefixing it buries that behind plumbing vocabulary.
	if IsOutdated(err) {
		return OutdatedConnectError(err)
	}

	// The machine's state explains the failure: its state picks the code. "No
	// machine at all" is FailedPrecondition here, not the Unavailable below —
	// waiting cannot produce a machine the user has not connected — and a
	// machine too busy to answer in time is DeadlineExceeded.
	if cerr, ok := MachineStateConnectError(fmt.Sprintf("daemon command %s failed", commandType), err); ok {
		return cerr
	}

	// The daemon answered, but the requested working directory is absent on its
	// filesystem. That's a missing resource / precondition on the request, not
	// an infrastructure outage, so it must not read as a retryable "unavailable".
	if strings.Contains(err.Error(), dirNotExistMarker) {
		return connect.NewError(connect.CodeNotFound,
			fmt.Errorf("daemon command %s: %w", commandType, err))
	}

	// Everything else — daemon unreachable, a transport failure, command not
	// registered on the daemon, unexpected daemon-side failure, or a daemon
	// that exists but hasn't connected yet (toolexec.ErrDaemonPending) —
	// surfaces as a loud, retryable Unavailable. The pending case is the one
	// the frontend's daemon-wait machinery specifically looks for via the
	// "no daemon connected" message marker (see toolexec.ErrDaemonPending).
	return connect.NewError(connect.CodeUnavailable,
		fmt.Errorf("daemon command %s failed: %w", commandType, err))
}
