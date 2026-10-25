// Copyright (c) 2025 Reliant Labs

// Package daemonerr classifies the failure of a command the server sent to a
// user's daemon as the svcerr kind a handler returns.
//
// A daemon command crosses NATS and comes back as an opaque error: the router
// flattens transport failures, timeouts, unresolved daemons, unknown commands
// and daemon-side handler errors into one error whose sentinel (toolexec) or
// text is the only discriminator. Every handler that forwards to a daemon asks
// this package what that failure means before choosing its own code, so the
// machine's state decides the wire code the same way everywhere.
//
// What the client reads follows one rule. The daemon's own words — a git
// rejection, a missing path, "update this machine" — come from the user's own
// machine to the same user and are the actionable part of the message, so
// they are kept. The router's and the transport's words (NATS errors, gateway
// round-trip failures, resolution plumbing) are not: they stay on the error as
// its cause, for the log, and never reach the wire. A machine state is
// reported in the sentence the router wrote for the user, which carries the
// markers the web keys on ("no daemon connected", "still starting",
// "suspended", "no daemon available").
//
//forge:exclude-contract: pure classification of daemon-command failures onto svcerr kinds; no I/O, state or time
package daemonerr

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Unreachable reports whether err says the request never reached its
// machine because the machine is not connected: resolution found it suspended
// or not yet attached (toolexec.ErrDaemonPending), or NATS had no responder on
// its subject (the "no daemon connected" Unavailable from the router). Either
// way nothing ran, so waking the machine and asking again is safe even for a
// write.
//
// The code is read with svcerr.Code, which sees a *connect.Error and a svcerr
// kind alike, so it answers the same for the router's error and for what
// Error made of it.
func Unreachable(err error) bool {
	if toolexec.IsDaemonPending(err) {
		return true
	}
	return svcerr.Code(err) == connect.CodeUnavailable &&
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

// Outdated maps a version-skew failure onto FailedPrecondition, preserving
// the daemon's own explanation.
//
// FailedPrecondition rather than Internal or Unavailable, deliberately:
// Internal says Reliant is broken and invites a bug report for something that
// is not a defect, and Unavailable invites a retry that cannot succeed until
// the daemon is updated. FailedPrecondition is the code that means "the system
// is not in the state this call requires" — which is exactly true, and is the
// only one of the three that points the user at the action that fixes it.
func Outdated(err error) error {
	return svcerr.WithCause(svcerr.FailedPrecondition(daemonText(err)), err)
}

// machineCode is the code a daemon-command failure that the machine's state
// explains calls for, and false for any other failure. MachineState and
// MachineStateConnectError both decide by it, so the two spellings cannot
// disagree on a code.
func machineCode(err error) (connect.Code, bool) {
	switch {
	case Unreachable(err):
		return connect.CodeUnavailable, true
	case toolexec.IsNoDaemon(err):
		return connect.CodeFailedPrecondition, true
	case toolexec.IsDaemonTimeout(err):
		return connect.CodeDeadlineExceeded, true
	default:
		return 0, false
	}
}

// machineReason is the reason (svcerr.ReasonHeader) a machine-state failure
// carries to the client: the router's own (toolexec stamps each state it
// reports — daemon_starting, daemon_suspended, daemon_not_connected,
// no_daemon), else, for an unreachable machine reported only in text, the
// generic daemon_not_connected. A timeout has none: it is not a state the web
// waits on.
//
// Read off the wire form because the reason must survive what MachineState
// does to err — it becomes the cause, and a cause's reason is not the
// client's (forge svcerr) — so it is re-stamped on the result explicitly.
func machineReason(code connect.Code, err error) string {
	if reason := svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader); reason != "" {
		return reason
	}
	if code == connect.CodeUnavailable {
		return errreason.DaemonNotConnected
	}
	return ""
}

// MachineState returns a daemon-command failure that the machine's state
// explains — rather than the command itself — as the svcerr kind that state
// calls for, its message prefixed with what the handler was doing (doing may be
// empty). ok is false for any other failure, which the handler maps itself.
//
//   - not reachable right now — starting, asleep, or not connected (NATS had
//     no responder) — is Unavailable: retryable, what the web's daemon-wait
//     machinery expects, and the message keeps the "no daemon connected"
//     marker isDaemonConnectingError keys on. It is a user error
//     (svcerr.ClassUser): nobody on the server side can connect the machine;
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
//
// The message is the router's sentence for the state (see the package doc),
// never its plumbing or the transport's text; err stays reachable as the
// cause, so errors.Is(result, toolexec.ErrDaemonPending) and the log both
// still see it.
func MachineState(doing string, err error) (error, bool) {
	code, ok := machineCode(err)
	if !ok {
		return nil, false
	}
	msg := prefixed(doing, svcerr.ToConnect(err).Message())
	var kind error
	switch code {
	case connect.CodeUnavailable:
		kind = svcerr.WithClass(svcerr.Unavailable(msg), svcerr.ClassUser)
	case connect.CodeFailedPrecondition:
		kind = svcerr.FailedPrecondition(msg)
	default:
		kind = svcerr.DeadlineExceeded(msg)
	}
	return svcerr.WithReason(svcerr.WithCause(kind, err), machineReason(code, err)), true
}

// Error converts a daemon-command failure into the svcerr kind its class calls
// for, saying what was being done: doing reads like "daemon command
// pkg.list_commands" or "open the OAuth helper". The daemon router flattens
// every failure — transport, timeout, unresolved daemon, unknown command, and
// daemon-side handler errors — into a single opaque error whose text is the
// only discriminator, so this classifies by sentinel first and substring
// second (the same approach fs_proxy uses for "path is a directory").
//
// The invariant that matters most: whatever the class, the caller gets a loud
// error, NEVER an empty success. A handler returns the result as it is; the
// svcerr interceptor puts it on the wire.
func Error(doing string, err error) error {
	// The daemon is older than this server and has no handler for the command.
	// Surfaced in the daemon's own words and WITHOUT the "<doing> failed"
	// wrapper: the daemon's message already names the command and states the
	// fix, and prefixing it buries that behind plumbing vocabulary.
	if IsOutdated(err) {
		return Outdated(err)
	}

	// The machine's state explains the failure: its state picks the code. "No
	// machine at all" is FailedPrecondition here, not the Unavailable below —
	// waiting cannot produce a machine the user has not connected — and a
	// machine too busy to answer in time is DeadlineExceeded.
	if serr, ok := MachineState(doing+" failed", err); ok {
		return serr
	}

	// The daemon answered, but the path the request named is absent on its
	// filesystem. That's a missing resource / precondition on the request, not
	// an infrastructure outage, so it must not read as a retryable "unavailable".
	if pathMissing(err) {
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrNotFound, prefixed(doing, daemonText(err))), err)
	}

	// Everything else — a transport failure, command not registered on the
	// daemon, an unexpected daemon-side failure — surfaces as a loud, retryable
	// Unavailable, with the daemon's own words when it said any.
	msg := doing + " failed"
	if text, ok := toolexec.DaemonReportedText(err); ok {
		msg = prefixed(msg, text)
	}
	return svcerr.WithCause(svcerr.Unavailable(msg), err)
}

// daemonText is the text of a failure recognised by a marker the daemon
// writes (version skew, a missing directory): the daemon's own report when the
// router carried one, else err's text, which in that case is the daemon's
// words as some other path relayed them.
func daemonText(err error) string {
	if text, ok := toolexec.DaemonReportedText(err); ok {
		return text
	}
	return err.Error()
}

// pathMissing reports whether a daemon command failed because the path it
// named does not exist on the daemon's machine: the pkg working-dir marker in
// whatever text carried it, or any missing-path report in the daemon's own
// words (toolexec.DaemonReportedMissingPath) — a forge project dir, an ENOENT.
// It used to match the pkg marker alone, so a forge.* command for a project
// folder the machine does not have came back Unavailable and was logged as a
// server fault.
func pathMissing(err error) bool {
	return strings.Contains(err.Error(), dirNotExistMarker) || toolexec.DaemonReportedMissingPath(err)
}

func prefixed(doing, msg string) string {
	if doing == "" {
		return msg
	}
	return doing + ": " + msg
}

// The *connect.Error spellings below predate Error and MachineState and are
// kept, byte for byte, for the handlers that still call them: the code is
// decided by the same rules, and the message is the old one — doing wrapped
// around err's whole text, router and transport words included. Handlers move
// to Error / MachineState in their own svcerr lane (research/forge-migration/
// 11-svcerr-plan.md §4), where the message change is reviewed with the
// handler's tests; these go when the last caller has.

// MachineStateConnectError is MachineState as the *connect.Error a handler
// returns, with err's whole text in the message. ok is false for any failure
// the machine's state does not explain. Deprecated: use MachineState.
func MachineStateConnectError(doing string, err error) (*connect.Error, bool) {
	code, ok := machineCode(err)
	if !ok {
		return nil, false
	}
	reason := machineReason(code, err)
	if doing != "" {
		err = fmt.Errorf("%s: %w", doing, err)
	}
	cerr := connect.NewError(code, err)
	if reason != "" {
		cerr.Meta().Set(svcerr.ReasonHeader, reason)
	}
	return cerr, true
}

// OutdatedConnectError maps a version-skew failure onto FailedPrecondition
// (see Outdated), with err's whole text as the message. Deprecated: use
// Outdated.
func OutdatedConnectError(err error) *connect.Error {
	return connect.NewError(connect.CodeFailedPrecondition, err)
}

// DispatchConnectError converts a SendDaemonCommand failure of commandType
// into a Connect error whose code reflects the failure class (see Error).
// Deprecated: use Error(fmt.Sprintf("daemon command %s", commandType), err).
func DispatchConnectError(commandType string, err error) *connect.Error {
	if IsOutdated(err) {
		return OutdatedConnectError(err)
	}
	if cerr, ok := MachineStateConnectError(fmt.Sprintf("daemon command %s failed", commandType), err); ok {
		return cerr
	}
	if pathMissing(err) {
		return connect.NewError(connect.CodeNotFound,
			fmt.Errorf("daemon command %s: %w", commandType, err))
	}
	return connect.NewError(connect.CodeUnavailable,
		fmt.Errorf("daemon command %s failed: %w", commandType, err))
}
