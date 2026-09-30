// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

// Close must ACTIVELY end every open daemon stream with Unavailable.
//
// The defect it pins: Close was a no-op, and DaemonServer.Stop's only other
// step was http.Server.Shutdown, which waits for active requests to finish.
// A daemon bidi stream never finishes on its own, so Shutdown always burned
// the full shutdown budget and the process then exited with every daemon's TCP
// connection cut — no status, and dead time for the whole budget on every
// gateway rollout.
//
// With Close ending streams, the handler returns promptly with Unavailable and
// the daemon redials immediately (see isFatalError in
// internal/toolexec/daemonruntime/runtime.go: Unavailable is recoverable).
func TestCloseEndsOpenDaemonStreamsWithUnavailable(t *testing.T) {
	svc := NewToolsDaemonService(nil)

	stream := newParkedStream()
	conn := newTestConn("user-1", "daemon-1", stream)

	svc.mu.Lock()
	svc.connections[conn.daemonID] = conn
	svc.userDaemons[conn.userID] = []string{conn.daemonID}
	svc.mu.Unlock()

	// handleIncoming is what the Connect handler blocks in for the life of
	// the stream. Its return value is the status the daemon receives.
	handlerErr := make(chan error, 1)
	go func() { handlerErr <- svc.handleIncoming(context.Background(), conn) }()

	// The stream is parked in Receive, exactly like a healthy idle daemon
	// between heartbeats.
	select {
	case err := <-handlerErr:
		t.Fatalf("handler returned before shutdown: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	svc.Close()

	select {
	case err := <-handlerErr:
		require.Error(t, err, "Close must end the stream with a status, not a nil return")
		require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err),
			"the daemon must see Unavailable so it redials to a live replica; "+
				"got %v", err)
		require.Contains(t, err.Error(), "draining")
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the open daemon stream — the handler is still parked in Receive, " +
			"which is what made gateway shutdown burn its entire budget")
	}
}

// Unavailable must stay on the daemon's reconnect path. If it were ever
// classified terminal, a gateway rollout would permanently disconnect every
// daemon instead of moving them to the surge pod.
func TestDrainingErrorIsRecoverableForDaemons(t *testing.T) {
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(errGatewayDraining))
	require.NotEqual(t, connect.CodeAborted, connect.CodeOf(errGatewayDraining),
		"Aborted is the supersede signal and is terminal — a draining gateway must not use it")
}

// Close with no connections must not panic or block; the gateway shuts down
// this way whenever no daemon happens to be attached.
func TestCloseWithNoConnectionsIsANoOp(t *testing.T) {
	svc := NewToolsDaemonService(nil)
	done := make(chan struct{})
	go func() { svc.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close blocked with no connections")
	}
}

// Close is idempotent: it runs from gateway shutdown while individual streams
// may already be ending on their own.
func TestCloseIsIdempotent(t *testing.T) {
	svc := NewToolsDaemonService(nil)
	conn := newTestConn("user-1", "daemon-1", newParkedStream())

	svc.mu.Lock()
	svc.connections[conn.daemonID] = conn
	svc.userDaemons[conn.userID] = []string{conn.daemonID}
	svc.mu.Unlock()

	svc.Close()
	svc.Close()

	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(conn.closedReason()),
		"the first Close's reason must survive the second")
}
