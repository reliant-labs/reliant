// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// daemonProxyBase is the shared forwarding plumbing for gRPC services that, on a
// cloud daemon, must run their work on the daemon's filesystem rather than the
// api-server's. A per-service proxy EMBEDS it and calls dispatch to forward a
// typed request to a registered daemon command (e.g. "pkg.list_commands").
//
// Its reason to exist is UNIFORM, LOUD error mapping: every failure mode —
// daemon unreachable, dispatch timeout, command not registered, malformed
// response — becomes a Connect error, never a silently-empty success. A silent
// empty result is precisely the class of bug this base was written to prevent
// (PackageCommandsService.ListCommands returning [] on a cloud daemon because
// discovery ran on the wrong filesystem).
//
// The existing FileSystem/Background/Terminal proxies predate this base and
// still carry their own copies of getUserID/sendCommand; they are structured so
// they can adopt this base later (tracked as a follow-up).
type daemonProxyBase struct {
	router toolexec.DaemonRouter
}

// userID pulls the authenticated user id from the request context, returning a
// Connect Unauthenticated error when absent.
func (b *daemonProxyBase) userID(ctx context.Context) (string, error) {
	uid, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	return uid, nil
}

// dispatch marshals req, forwards it to the daemon as commandType, and
// unmarshals the reply into resp. Any failure is mapped onto a loud Connect
// error via mapDaemonDispatchError; dispatch never returns nil while leaving
// resp unpopulated, so callers can rely on "no error => resp is valid".
func (b *daemonProxyBase) dispatch(
	ctx context.Context,
	userID, commandType string,
	req, resp any,
	timeoutMs int32,
) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("marshal %s request: %w", commandType, err))
	}

	respBytes, err := b.router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	if err != nil {
		return mapDaemonDispatchError(commandType, err)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal %s response: %w", commandType, err))
	}
	return nil
}

// daemonErrDirNotExistMarker mirrors daemonruntime's pkgDirNotExistPrefix. The
// two constants live in different packages (the proxy must not import the daemon
// runtime), and the error text is the wire contract between them: daemon-command
// errors cross the transport as plain strings, not wrapped Go errors.
const daemonErrDirNotExistMarker = "working dir does not exist"

// daemonErrOutdatedMarker mirrors the phrase daemonruntime's
// unknownCommandError builds. Same wire-contract reasoning as above: the text
// is all that survives the transport.
//
// A daemon reporting this is older than the server asking, which is a state the
// user has to resolve by updating their machine — retrying cannot help.
const daemonErrOutdatedMarker = "older version of Reliant"

// IsDaemonOutdatedError reports whether err is a daemon-too-old failure, i.e.
// the server asked for a command the daemon's build does not have.
func IsDaemonOutdatedError(err error) bool {
	return err != nil && strings.Contains(err.Error(), daemonErrOutdatedMarker)
}

// DaemonOutdatedConnectError maps a version-skew failure onto
// FailedPrecondition, preserving the daemon's own explanation.
//
// FailedPrecondition rather than Internal or Unavailable, deliberately:
// Internal says Reliant is broken and invites a bug report for something that
// is not a defect, and Unavailable invites a retry that cannot succeed until
// the daemon is updated. FailedPrecondition is the code that means "the system
// is not in the state this call requires" — which is exactly true, and is the
// only one of the three that points the user at the action that fixes it.
func DaemonOutdatedConnectError(err error) *connect.Error {
	return connect.NewError(connect.CodeFailedPrecondition, err)
}

// mapDaemonDispatchError converts a SendDaemonCommand failure into a Connect
// error whose code reflects the failure class. SendDaemonCommand flattens every
// failure — transport, timeout, unresolved daemon, unknown command, and
// daemon-side handler errors — into a single opaque error whose text is the only
// discriminator, so this classifies by substring (the same approach fs_proxy
// uses for "path is a directory").
//
// The invariant that matters most: whatever the class, the caller gets a loud
// Connect error, NEVER an empty success.
func mapDaemonDispatchError(commandType string, err error) *connect.Error {
	// The daemon is older than this server and has no handler for the command.
	// Surfaced verbatim and WITHOUT the "daemon command X failed" wrapper: the
	// daemon's message already names the command and states the fix, and
	// prefixing it buries that behind plumbing vocabulary.
	if IsDaemonOutdatedError(err) {
		return DaemonOutdatedConnectError(err)
	}

	// The daemon answered, but the requested working directory is absent on its
	// filesystem. That's a missing resource / precondition on the request, not
	// an infrastructure outage, so it must not read as a retryable "unavailable".
	if strings.Contains(err.Error(), daemonErrDirNotExistMarker) {
		return connect.NewError(connect.CodeNotFound,
			fmt.Errorf("daemon command %s: %w", commandType, err))
	}

	// Everything else — daemon unreachable, dispatch timeout, command not
	// registered on the daemon, unexpected daemon-side failure, or a daemon
	// that exists but hasn't connected yet (toolexec.ErrDaemonPending) —
	// surfaces as a loud, retryable Unavailable. The pending case is the one
	// the frontend's daemon-wait machinery specifically looks for via the
	// "no daemon connected" message marker (see toolexec.ErrDaemonPending).
	return connect.NewError(connect.CodeUnavailable,
		fmt.Errorf("daemon command %s failed: %w", commandType, err))
}
