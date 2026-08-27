// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

// A daemon whose id was claimed by another daemon PROCESS must stop, not
// redial. Redialing evicts the new holder, which redials and evicts this one:
// two local daemons sharing one id flapped every 15-30s for 37 minutes on
// 2026-07-27, dropping every in-flight tool request each time.
//
// The gateway signals this with CodeAborted (see supersedeIncumbent in
// internal/grpc/services). This test asserts the code, not the message, so it
// survives a reword and fails on a rewire.
func TestSupersededDaemonDoesNotReconnect(t *testing.T) {
	superseded := connect.NewError(connect.CodeAborted,
		errors.New("daemon identity claimed by another daemon process"))

	require.True(t, isFatalError(superseded),
		"a superseded daemon that reconnects re-enters the eviction loop it was just removed from")

	// It must survive the wrapping the session loop applies before the check.
	wrapped := fmt.Errorf("daemon stream receive: %w", superseded)
	require.True(t, isFatalError(wrapped))
}

// Only supersession is terminal. Ordinary stream loss — EOF, a gateway
// restart, a laptop waking up — must still reconnect, or a transient blip
// leaves the machine with no daemon at all.
func TestOrdinaryStreamLossStillReconnects(t *testing.T) {
	for name, err := range map[string]error{
		"eof":         errors.New("daemon stream receive: unknown: EOF"),
		"unavailable": connect.NewError(connect.CodeUnavailable, errors.New("connection refused")),
		"internal":    connect.NewError(connect.CodeInternal, errors.New("boom")),
		"canceled":    connect.NewError(connect.CodeCanceled, errors.New("canceled")),
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, isFatalError(err))
		})
	}
}

// Credentials the gateway would not accept must NOT be terminal.
//
// This was fatal until 2026-08-24, and the failure it produced was total: a
// dev-k8s deploy repointed the gateway at a different database, so the
// daemon's PAT was looked up where it had never been written and came back
// "invalid daemon auth token: invalid token". The daemon stopped and logged
// nothing for 10 hours while the app reported "no machine is connected".
// Repairing the gateway changed nothing — no daemon was left running to
// notice. Only killing the process, so its supervisor respawned it, recovered.
//
// Every cause of Unauthenticated is repaired AROUND a running daemon (the
// supervisor re-mints the PAT, the operator fixes the gateway's config, a
// rollout finishes), so the daemon has to still be trying when that happens.
func TestUnauthenticatedReconnects(t *testing.T) {
	unauth := connect.NewError(connect.CodeUnauthenticated,
		errors.New("invalid daemon auth token: invalid token"))

	require.False(t, isFatalError(unauth),
		"a daemon that stops on Unauthenticated cannot recover when its PAT is re-minted — "+
			"it is not running to retry, and nothing restarts it")

	// The session loop wraps before classifying; the real error arrived as
	// "daemon stream receive: unauthenticated: invalid daemon auth token".
	wrapped := fmt.Errorf("daemon stream receive: %w", unauth)
	require.False(t, isFatalError(wrapped))
}

// PermissionDenied stays terminal, and the distinction is the point:
// Unauthenticated means the credentials were not accepted (fixable around the
// daemon), PermissionDenied means they were accepted and the answer was still
// no (redialing cannot change it).
func TestPermissionDeniedStillTerminal(t *testing.T) {
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("not allowed"))
	require.True(t, isFatalError(denied))
	require.True(t, isFatalError(fmt.Errorf("daemon stream receive: %w", denied)))
}
