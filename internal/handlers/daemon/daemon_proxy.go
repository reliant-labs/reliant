// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Every rpc forwards its request to the caller's daemon as one DaemonCommand
// (request/response) through Deps.Router.

// errNoRouter answers every rpc of a service composed without a daemon
// router: this server has no way to reach a daemon.
func errNoRouter() error {
	return svcerr.Unavailable("no daemon router: this server cannot reach a daemon")
}

// oauthDaemonError maps a failed OAuth daemon command onto the svcerr kind
// for what went wrong, saying what was being done:
//
//   - The daemon is older than this server: FailedPrecondition, the daemon's
//     own message. The production report that started this was
//     `open OAuth helper: daemon command "auth.open_oauth_helper" failed:
//     unknown daemon command type` from a daemon that predated the handler —
//     nothing was broken server-side, but `code: internal` said otherwise.
//   - The command never reached a machine: the machine's state picks the code
//     (daemonerr.MachineState). "No machine is connected to your account
//     yet" used to leave here as Internal (ELECTRON-B1); the settings page
//     asks for the helper whether or not the user has connected a machine.
//   - Anything else — the daemon ran the command and it failed, the reply
//     timed out — is Internal. The daemon's own words reach the user; the
//     router's and the transport's stay on the error as its logged cause.
func oauthDaemonError(doing string, err error) error {
	if daemonerr.IsOutdated(err) {
		return daemonerr.Outdated(err)
	}
	if serr, ok := daemonerr.MachineState(doing, err); ok {
		return serr
	}
	msg := doing
	if text, ok := toolexec.DaemonReportedText(err); ok {
		msg = doing + ": " + text
	}
	return svcerr.WithCause(svcerr.Internal(msg), err)
}

// openHelperTimeoutMs bounds the open/close commands. These return as soon as
// the listener is bound or closed — no human is in the loop — so the generous
// hour StartOAuthFlow needs would only delay reporting a dead daemon.
const openHelperTimeoutMs = 10_000
