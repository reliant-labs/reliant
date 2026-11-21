// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Every rpc forwards its request to the caller's daemon through Deps.Router as
// a DaemonCommand (request/response). Terminal I/O is the websocket
// (TerminalWSHandler), not an rpc.

func (s *Service) getUserID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", svcerr.Unauthenticated("user ID not found in context")
	}
	return userID, nil
}

// terminalCloseTimeout bounds the close a terminal connection sends as it
// tears down, so a daemon that cannot be reached does not hold the handler.
const terminalCloseTimeout = 10 * time.Second

// closeDaemonTerminalSession closes the daemon terminal session that a
// terminal connection (TerminalWSHandler) created. The handler defers it as
// soon as it has a session id.
//
// A session is owned by the connection that created it. Every connection
// sends terminal.create and nothing can reattach to an existing session, so
// once its connection is gone a session is unreachable — but its login shell
// and PTY stay alive on the user's machine until the daemon exits. The
// handlers used to only unsubscribe on the way out, so every dropped
// connection (reload, sleep, network blip, API restart) leaked a shell, and
// the browser's reconnect created another. One daemon accumulated 501,
// exhausting macOS's machine-wide PTY pool (kern.tty.ptmx_max = 511) so that
// no program on the machine could open a terminal.
//
// The request context is already cancelled by the time this runs, so the
// close is sent on a detached, bounded context that keeps its values. It is
// best-effort: the session is often already gone (the shell exited, or the
// client closed it through CloseSession first), and the daemon's session cap
// is the backstop when the close cannot be delivered at all.
// daemonID is the machine the session was created on ("" for default
// resolution): a session lives on one machine.
func closeDaemonTerminalSession(ctx context.Context, router SessionRouter, userID, daemonID, sessionID string) {
	if sessionID == "" {
		return
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalCloseTimeout)
	defer cancel()

	payload, err := json.Marshal(map[string]any{"session_id": sessionID})
	if err != nil {
		logging.ErrorContext(ctx, "[Terminal] Failed to marshal session close", "error", err, "session_id", sessionID)
		return
	}
	if _, err := sendToMachine(closeCtx, router, userID, daemonID, "terminal.close", payload, int32(terminalCloseTimeout/time.Millisecond)); err != nil {
		logging.DebugContext(ctx, "[Terminal] Session close on disconnect did not complete (may already be closed)",
			"error", err, "session_id", sessionID)
	}
}

// sendToMachine sends a daemon command to daemonID, or by default resolution
// when it is "".
func sendToMachine(ctx context.Context, router SessionRouter, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if daemonID == "" {
		return router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	}
	return router.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
}

// daemonFailure is a failed daemon round trip as the svcerr kind the rpc
// returns. A machine that is starting, asleep or absent is not a server
// fault: its state picks the code, the message prefixed with machineDoing
// (daemonerr.MachineState; may be empty). A daemon older than this server is
// FailedPrecondition in the daemon's own words (daemonerr.Outdated). Anything
// else is Internal, saying what was being done: the daemon's own words reach
// the user, the router's and the transport's stay on the error as its logged
// cause.
func daemonFailure(machineDoing, doing string, err error) error {
	if daemonerr.IsOutdated(err) {
		return daemonerr.Outdated(err)
	}
	if serr, ok := daemonerr.MachineState(machineDoing, err); ok {
		return serr
	}
	msg := doing + " failed"
	if text, ok := toolexec.DaemonReportedText(err); ok {
		msg += ": " + text
	}
	return svcerr.WithCause(svcerr.Internal(msg), err)
}
