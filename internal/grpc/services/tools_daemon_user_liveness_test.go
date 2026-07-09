// Copyright (c) 2025 Reliant Labs

package services

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestConnectedDaemonCountForUser verifies the daemonquery.UserLivenessSource
// implementation the per-user any-live NATS responder answers from: the count
// must track the in-memory connection map exactly, including through the real
// removeConnection path, and stay isolated per user.
//
// Deliberately hermetic: nothing in this path touches the database, so the
// service is built with a nil repo and the test runs without DATABASE_URL.
func TestConnectedDaemonCountForUser(t *testing.T) {
	svc := NewToolsDaemonService(nil)
	defer svc.Close()

	const userA = "user-a"
	const userB = "user-b"
	require.Equal(t, 0, svc.ConnectedDaemonCountForUser(userA))

	connA1 := &daemonConnection{userID: userA, daemonID: uuid.New().String()}
	connA2 := &daemonConnection{userID: userA, daemonID: uuid.New().String()}
	connB1 := &daemonConnection{userID: userB, daemonID: uuid.New().String()}
	svc.mu.Lock()
	registerTestConn(svc, connA1)
	registerTestConn(svc, connA2)
	registerTestConn(svc, connB1)
	svc.mu.Unlock()

	require.Equal(t, 2, svc.ConnectedDaemonCountForUser(userA))
	require.Equal(t, 1, svc.ConnectedDaemonCountForUser(userB))

	// Remove via the real removal path (what teardownConnection uses).
	require.True(t, svc.removeConnection(connA1.daemonID, userA, connA1))
	require.Equal(t, 1, svc.ConnectedDaemonCountForUser(userA))

	// Removing a connection that has already been superseded must not change
	// the count (reconnect-takeover race guard).
	require.False(t, svc.removeConnection(connA2.daemonID, userA, connA1))
	require.Equal(t, 1, svc.ConnectedDaemonCountForUser(userA))

	require.True(t, svc.removeConnection(connA2.daemonID, userA, connA2))
	require.Equal(t, 0, svc.ConnectedDaemonCountForUser(userA))

	// Other users are unaffected.
	require.Equal(t, 1, svc.ConnectedDaemonCountForUser(userB))
}
