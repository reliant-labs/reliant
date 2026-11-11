// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DisconnectDaemon ends the daemon stream this replica holds for daemonID,
// because the authority it connected with is gone (the control plane's
// disconnect command, servergateway.DisconnectCommandSubscriber). It reports
// whether it ended one.
//
// Every replica is asked and at most one holds the stream, so a daemon this
// replica does not hold is the common case and a no-op. So is a repeat: the
// first call removed the connection.
//
// The stream ends with Unauthenticated, the status the daemon's reconnect will
// get from the revoked credential anyway. The daemon keeps redialing on it
// (daemonruntime's run loop: Unauthenticated is recoverable, because the fix,
// a re-minted credential, happens around a running daemon), so a daemon whose
// operator is given access again recovers without a restart.
//
// Only an INBOUND stream is ended. An outbound one is a managed machine the
// gateway dialed itself, for a pod the control plane's reconciler named; no
// credential authenticated it, and ending it would only make the connector
// redial it a second later. What ends a managed machine is its lifecycle
// (suspend, delete), which the reconciler already follows.
func (s *Service) DisconnectDaemon(daemonID, reason string) bool {
	daemonID = strings.TrimSpace(daemonID)
	if daemonID == "" {
		return false
	}
	s.mu.RLock()
	conn := s.connections[daemonID]
	s.mu.RUnlock()
	if conn == nil {
		return false
	}
	if conn.attachSource == db.DaemonAttachmentSourceOutbound {
		logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Disconnect command names an outbound (gateway-dialed) daemon stream; leaving it to the managed reconciler",
			"daemonID", daemonID, "userID", conn.userID, "reason", reason)
		return false
	}
	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Ending daemon stream on the control plane's disconnect command",
		"daemonID", daemonID, "userID", conn.userID, "reason", reason)
	// The reason first, then the same teardown the sweeper runs: the stream
	// handler returns the reason to the daemon, and teardown makes the daemon
	// unroutable at once rather than when the handler gets round to it.
	conn.closeWith(errAuthorityRevoked(reason))
	s.teardownConnection(conn, "disconnect command: "+reason)
	return true
}

// errAuthorityRevoked is the status a stream ended by the disconnect command
// returns to its daemon. Unauthenticated is a user-class error (svcerr), so
// ending a revoked daemon's stream is not logged as a failed stream.
func errAuthorityRevoked(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "access revoked"
	}
	return svcerr.Unauthenticated("daemon-gateway ended this stream: " + reason +
		"; the credential it connected with no longer authenticates")
}
