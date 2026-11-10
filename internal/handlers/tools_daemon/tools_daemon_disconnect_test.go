// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// holdConn registers conn in svc's connection map, as a registration does.
func holdConn(svc *Service, conn *daemonConnection) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.connections[conn.daemonID] = conn
	svc.userDaemons[conn.userID] = append(svc.userDaemons[conn.userID], conn.daemonID)
}

// The disconnect command ends the named daemon's stream with Unauthenticated
// (the daemon redials on it, and its revoked credential is refused there),
// removes it from the routing map at once, and leaves every other daemon's
// stream alone.
func TestDisconnectDaemonEndsOnlyTheNamedStream(t *testing.T) {
	svc := newDaemonlessService(t)
	revoked := newTestConn("user-1", uuid.NewString(), newParkedStream())
	other := newTestConn("user-1", uuid.NewString(), newParkedStream())
	holdConn(svc, revoked)
	holdConn(svc, other)

	handlerErr := make(chan error, 1)
	go func() { handlerErr <- svc.handleIncoming(context.Background(), revoked) }()

	require.True(t, svc.DisconnectDaemon(revoked.daemonID, "credential revoked"))

	select {
	case err := <-handlerErr:
		require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "got %v", err)
		require.Contains(t, err.Error(), "credential revoked", "the daemon is told why")
	case <-time.After(5 * time.Second):
		t.Fatal("the disconnected daemon's stream handler did not return")
	}
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(revoked.closedReason()),
		"teardown must not overwrite the reason with an ordinary close")

	_, _, held := svc.DaemonStatus(revoked.daemonID)
	require.False(t, held, "the disconnected daemon is unroutable at once")
	_, _, held = svc.DaemonStatus(other.daemonID)
	require.True(t, held, "an unrelated daemon stays connected")
	require.Nil(t, other.closedReason())
	select {
	case <-other.done:
		t.Fatal("an unrelated daemon's stream was ended")
	default:
	}
}

// Every replica hears every command and at most one holds the stream, so a
// daemon this replica does not hold, an empty id and a repeat are no-ops.
func TestDisconnectDaemonUnknownOrRepeatedIsANoOp(t *testing.T) {
	svc := newDaemonlessService(t)
	require.False(t, svc.DisconnectDaemon(uuid.NewString(), "credential revoked"))
	require.False(t, svc.DisconnectDaemon("  ", "credential revoked"))

	conn := newTestConn("user-1", uuid.NewString(), newParkedStream())
	holdConn(svc, conn)
	require.True(t, svc.DisconnectDaemon(conn.daemonID, "member removed from org"))
	require.False(t, svc.DisconnectDaemon(conn.daemonID, "member removed from org"),
		"a repeat finds nothing left to end")
}

// An outbound stream is one the gateway dialed for a managed machine the
// reconciler named; no credential authenticated it, and the connector would
// redial it at once, so the command leaves it alone.
func TestDisconnectDaemonLeavesOutboundStreams(t *testing.T) {
	svc := newDaemonlessService(t)
	conn := newTestConn("user-1", uuid.NewString(), newParkedStream())
	conn.attachSource = db.DaemonAttachmentSourceOutbound
	holdConn(svc, conn)

	require.False(t, svc.DisconnectDaemon(conn.daemonID, "credential revoked"))
	require.Nil(t, conn.closedReason())
	_, _, held := svc.DaemonStatus(conn.daemonID)
	require.True(t, held)
}

// The status is a client-class Unauthenticated, so ending a revoked daemon's
// stream is not logged as a failed stream, and it is not PermissionDenied,
// which would stop the daemon for good.
func TestAuthorityRevokedErrorIsRecoverableUnauthenticated(t *testing.T) {
	err := errAuthorityRevoked("")
	require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err))
	require.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
	require.Contains(t, err.Error(), "access revoked", "an empty reason still says what happened")
}
