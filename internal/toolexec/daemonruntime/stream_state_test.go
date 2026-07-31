// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// ackingGateway accepts a daemon stream and acknowledges the registration —
// the message that means "your stream is established".
type ackingGateway struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
}

func (g *ackingGateway) ConnectDaemon(ctx context.Context, stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]) error {
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
	<-ctx.Done()
	return ctx.Err()
}

// waitForStream polls the daemon's runtime record until it reports want.
func waitForStream(t *testing.T, dataDir string, want daemonstate.Stream) daemonstate.State {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last daemonstate.State
	for time.Now().Before(deadline) {
		state, err := daemonstate.Read(dataDir)
		if err == nil {
			last = state
			if state.Stream == want {
				return state
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon never recorded stream %q (last record: %+v)", want, last)
	return last
}

func newDaemonClientForTest(t *testing.T, dataDir, gatewayURL string) *daemonClient {
	t.Helper()
	t.Setenv("DAEMON_WORKING_DIR", t.TempDir())
	client, err := newDaemonClient(bootstrap.DaemonBootstrapConfig{
		AuthToken: "rlnt_pat_test",
		GRPCURL:   gatewayURL,
		TLSMode:   bootstrap.TLSModeH2C,
		DataDir:   dataDir,
	})
	require.NoError(t, err)
	return client
}

// A daemon whose gateway is unreachable must record that its stream is down.
// Without this the only local evidence a daemon exists is a PID, which is true
// of a daemon that has never connected and can serve no tool call.
func TestDaemonRecordsStreamDownWhenGatewayUnreachable(t *testing.T) {
	dataDir := t.TempDir()

	// A port that is bound and then released is reliably closed.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	deadURL := "http://" + listener.Addr().String()
	require.NoError(t, listener.Close())

	client := newDaemonClientForTest(t, dataDir, deadURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.run(ctx) }()

	state := waitForStream(t, dataDir, daemonstate.StreamDisconnected)
	require.False(t, state.Stream.Established())
	require.NotEmpty(t, state.StreamDetail, "the error that ended the stream must be recorded")
	require.True(t, state.ConnectedAt.IsZero(), "a daemon that never connected must not claim it did")
}

// The gateway's RegistrationAck is the one unambiguous "stream is up" signal;
// a successful local Send proves nothing about the far end.
func TestDaemonRecordsStreamEstablishedOnRegistrationAck(t *testing.T) {
	dataDir := t.TempDir()

	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewToolsDaemonServiceHandler(&ackingGateway{}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	defer srv.Close()

	client := newDaemonClientForTest(t, dataDir, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.run(ctx) }()

	state := waitForStream(t, dataDir, daemonstate.StreamConnected)
	require.True(t, state.Stream.Established())
	require.False(t, state.ConnectedAt.IsZero())
	require.Empty(t, state.StreamDetail)
}

// `forge cluster urls` prints the gateway as grpc://host:port. A daemon pointed
// at that form must connect, not run forever without a stream.
func TestDaemonConnectsToGRPCSchemeGateway(t *testing.T) {
	dataDir := t.TempDir()

	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewToolsDaemonServiceHandler(&ackingGateway{}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	defer srv.Close()

	client := newDaemonClientForTest(t, dataDir, "grpc://"+srv.URL[len("http://"):])

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.run(ctx) }()

	state := waitForStream(t, dataDir, daemonstate.StreamConnected)
	require.True(t, state.Stream.Established())
}
