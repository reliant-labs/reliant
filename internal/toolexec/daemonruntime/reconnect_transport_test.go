// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// countingListener records how many TCP connections were accepted, which is
// the only place the leak is visible: the daemon's own logs and the gateway's
// stream accounting both look identical whether the sessions shared one
// connection or opened a new one each time.
type countingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

// hangUpGateway acks the registration and then ends the stream, so the daemon
// reconnects. The HTTP/2 connection underneath is left open and reusable —
// exactly what a gateway does when it supersedes or reaps one stream.
type hangUpGateway struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	sessions atomic.Int64
}

func (g *hangUpGateway) ConnectDaemon(_ context.Context, stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]) error {
	if _, err := stream.Receive(); err != nil {
		return err
	}
	if err := stream.Send(&reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_RegistrationAck{
			RegistrationAck: &reliantv1.RegistrationAck{Accepted: true, DaemonId: "daemon-1"},
		},
	}); err != nil {
		return err
	}
	g.sessions.Add(1)
	return nil
}

// A reconnecting daemon must reuse its HTTP/2 connection pool rather than
// build a new transport per session.
//
// Each transport owns its own pool, so one built per session left the previous
// connection ESTABLISHED for the life of the process, with its reader and
// writer goroutines, on both ends. Observed 2026-07-27 against the dev
// gateway: a daemon with 8 sessions held 8 sockets to :29190 and one with ~60
// sessions held ~60 — a 1:1 match with the session count, none of them
// carrying a stream. A flapping daemon is exactly when this compounds, and a
// long forge run is exactly when a flap happens.
func TestReconnectingDaemonReusesOneGatewayConnection(t *testing.T) {
	gateway := &hangUpGateway{}

	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewToolsDaemonServiceHandler(gateway))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	listener := &countingListener{Listener: srv.Listener}
	srv.Listener = listener
	srv.EnableHTTP2 = true
	srv.Start()
	defer srv.Close()

	client := newDaemonClientForTest(t, t.TempDir(), srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.run(ctx) }()

	// Three sessions is enough to tell reuse from per-session dialing, and the
	// daemon's reconnect backoff (1s, then 2s) keeps it under a few seconds.
	const wantSessions = 3
	deadline := time.Now().Add(30 * time.Second)
	for gateway.sessions.Load() < wantSessions {
		if time.Now().After(deadline) {
			t.Fatalf("daemon reconnected only %d times in 30s, wanted %d", gateway.sessions.Load(), wantSessions)
		}
		time.Sleep(20 * time.Millisecond)
	}

	require.Equal(t, int64(1), listener.accepted.Load(),
		"%d daemon sessions must share one TCP connection to the gateway; a new one per session leaks the previous one for the life of the process",
		gateway.sessions.Load())
}
