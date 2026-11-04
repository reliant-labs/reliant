// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// statusAtDisconnect records what the gateway's status source (the one
// daemonquery.Responder answers from) reports while each listener hears a
// disconnect.
type statusAtDisconnect struct {
	svc *Service
	mu  sync.Mutex
	ok  []bool
}

func (l *statusAtDisconnect) OnDaemonConnected(string, string) {}

func (l *statusAtDisconnect) OnDaemonDisconnected(_, daemonID string) {
	_, _, ok := l.svc.DaemonStatus(daemonID)
	l.mu.Lock()
	l.ok = append(l.ok, ok)
	l.mu.Unlock()
}

// By the time any listener hears a disconnect — the DAEMON_EVENTS publisher,
// registered first, publishes daemon.v1.events.disconnected from it — this
// gateway has stopped claiming the daemon: DaemonStatus reports ok=false, so
// its status responder stays silent for the dropped stream even before it
// unsubscribes.
//
// The control plane depends on this (control-plane #1834): on a disconnect
// event it asks daemon.v1.query.<id>.status who holds the daemon, and treats a
// "connected" reply as proof the event is a stale disconnect of a stream
// another replica has since taken over. If this gateway answered for its own
// dropped stream, a genuine disconnect would be ignored.
func TestTeardownConnection_ListenersHearTheDisconnectAfterTheStatusSourceDropsTheDaemon(t *testing.T) {
	svc := newTestService(listChangedRepo(nil))
	t.Cleanup(svc.Close)
	rec := &statusAtDisconnect{svc: svc}
	svc.AddConnectionListener(rec)

	conn := newTestConn("user-1", "daemon-1", newParkedStream())
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()
	_, _, held := svc.DaemonStatus("daemon-1")
	require.True(t, held, "fixture: the gateway holds the daemon before teardown")

	svc.teardownConnection(conn, "test")

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Equal(t, []bool{false}, rec.ok, "a listener heard the disconnect while the status source still claimed the daemon")
}
