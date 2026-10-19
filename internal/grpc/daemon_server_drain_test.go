// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/pkg/config"
)

// A daemon-gateway rollout, through the real listener: DaemonServer.Stop is
// make-before-break. Every held daemon is told to move; the drain waits for
// the daemons to close their old streams, and ends whatever is still open at
// its deadline with Unavailable — the status a daemon redials on — never with
// the OK a watch stream ends with, and never by cutting the connection.

// startDrainGateway serves the REAL daemon gateway (NewDaemonServer) on a
// loopback port and returns it with its URL and a daemon credential.
func startDrainGateway(t *testing.T) (*DaemonServer, string, string) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	authority := tokenauthority.NewMemory()
	gateway, err := tools_daemon.New(tools_daemon.Deps{Logger: slog.Default(), Config: &config.Config{}, Repo: repo})
	require.NoError(t, err)
	ds := NewDaemonServer(&DaemonConfig{ToolsDaemonService: gateway, DaemonTokens: authority})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- ds.server.Serve(ln) }()
	t.Cleanup(func() {
		_ = ds.server.Close()
		<-served
	})

	minted, err := authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: "user-drain", Name: "drain", Scopes: []fat.Scope{fat.ScopeDaemonConnect},
	})
	require.NoError(t, err)
	return ds, "http://" + ln.Addr().String(), minted.Plaintext
}

// attachMovingDaemon opens ConnectDaemon as a current daemon (one that acts
// on GatewayDrain) and waits for the gateway to acknowledge the registration.
func attachMovingDaemon(t *testing.T, gatewayURL, bearer string) *connect.BidiStreamForClient[reliantv1.DaemonMessage, reliantv1.ServerMessage] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := reliantv1connect.NewToolsDaemonServiceClient(priorKnowledgeClient(), gatewayURL, connect.WithGRPC())
	stream := client.ConnectDaemon(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+bearer)
	t.Cleanup(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	})
	require.NoError(t, stream.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Register{
		Register: &reliantv1.DaemonRegister{Hostname: "drain-host", Platform: "linux", DaemonType: "local", MovesOnDrain: true},
	}}))
	nextMessage(t, stream, "the registration ack", func(m *reliantv1.ServerMessage) bool { return m.GetRegistrationAck() != nil })
	return stream
}

// nextMessage receives until match accepts a message, skipping heartbeats and
// anything else the gateway sends meanwhile.
func nextMessage(t *testing.T, stream *connect.BidiStreamForClient[reliantv1.DaemonMessage, reliantv1.ServerMessage], what string, match func(*reliantv1.ServerMessage) bool) *reliantv1.ServerMessage {
	t.Helper()
	for {
		msg, err := stream.Receive()
		require.NoError(t, err, "the stream ended before %s", what)
		if match(msg) {
			return msg
		}
	}
}

// stopInBackground runs Stop under budget, returning how it ended and how long
// it took.
type stopResult struct {
	err     error
	elapsed time.Duration
}

func stopInBackground(ds *DaemonServer, budget time.Duration) <-chan stopResult {
	done := make(chan stopResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		start := time.Now()
		err := ds.Stop(ctx)
		done <- stopResult{err: err, elapsed: time.Since(start)}
	}()
	return done
}

// The drain returns as soon as the daemon has moved: it waits for the old
// stream to close rather than for its deadline, and the notice names when
// the stream would otherwise have been ended.
func TestDaemonServerStopWaitsForTheDaemonToMove(t *testing.T) {
	ds, url, bearer := startDrainGateway(t)
	stream := attachMovingDaemon(t, url, bearer)

	const budget = 20 * time.Second
	stopped := stopInBackground(ds, budget)
	notice := nextMessage(t, stream, "the drain notice", func(m *reliantv1.ServerMessage) bool { return m.GetDrain() != nil }).GetDrain()
	until := time.Until(time.UnixMilli(notice.GetDeadlineUnixMs()))
	require.InDelta(t, (budget - serverkit.DefaultDrainReserve).Seconds(), until.Seconds(), 2,
		"the notice names the drain's hard deadline: the shutdown budget less its reserve")

	select {
	case r := <-stopped:
		t.Fatalf("Stop returned (%v) while the daemon's stream was still open", r.err)
	case <-time.After(200 * time.Millisecond):
	}

	// The daemon is registered elsewhere and retires its old stream.
	require.NoError(t, stream.CloseRequest())
	r := <-stopped
	require.NoError(t, r.err)
	require.Less(t, r.elapsed, budget/2, "Stop must return once the daemon moved, not wait out its deadline")
}

// A stream still open at the drain's deadline is ended with Unavailable —
// the daemon's redial signal — marked as drained.
func TestDaemonServerStopEndsAStreamThatDidNotMoveWithUnavailable(t *testing.T) {
	ds, url, bearer := startDrainGateway(t)
	stream := attachMovingDaemon(t, url, bearer)

	// Under twice the reserve, the drain keeps half the budget back: the
	// stream is ended at 1.5s.
	const budget = 3 * time.Second
	stopped := stopInBackground(ds, budget)
	nextMessage(t, stream, "the drain notice", func(m *reliantv1.ServerMessage) bool { return m.GetDrain() != nil })

	var err error
	for err == nil {
		_, err = stream.Receive()
	}
	require.False(t, errors.Is(err, io.EOF), "the stream must end with a status, not a clean end")
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "Unavailable puts the daemon on its reconnect path")
	require.Equal(t, "true", stream.ResponseTrailer().Get(serverkit.DrainedTrailer))

	r := <-stopped
	require.NoError(t, r.err, "the drain ended the stream itself; nothing was left for Shutdown to wait on")
	require.Less(t, r.elapsed, budget)
}
