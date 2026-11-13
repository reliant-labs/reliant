// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/pkg/config"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// disconnectGateway is one gateway replica's daemon side, end to end: the
// REAL daemon listener (credential auth, the daemon chain, ToolsDaemonService)
// on a loopback port, and the disconnect subscriber on a real NATS server.
type disconnectGateway struct {
	url       string
	service   *tools_daemon.Service
	authority *tokenauthority.Memory
	publisher *nats.Conn
}

func startDisconnectGateway(t *testing.T) *disconnectGateway {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	authority := tokenauthority.NewMemory()
	svc, err := tools_daemon.New(tools_daemon.Deps{Logger: slog.Default(), Config: &config.Config{}, Repo: repo})
	require.NoError(t, err)

	daemonAuth, err := grpcserver.NewDaemonAuth(authority)
	require.NoError(t, err)
	chain, err := grpcserver.NewDaemonChain(grpcserver.DaemonChainDeps{Auth: daemonAuth})
	require.NoError(t, err)
	listener, err := grpcserver.NewDaemonListener(grpcserver.DaemonListenerDeps{
		BindAddress: "127.0.0.1",
		Chain:       chain,
		Routes: serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
			svc.Register(mux, opts...)
			return []string{reliantv1connect.ToolsDaemonServiceName}
		}),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, listener.Listen(ctx))
	ran := make(chan error, 1)
	go func() { ran <- listener.Run(ctx) }()
	t.Cleanup(func() {
		svc.Close()
		cancel()
		<-ran
	})

	opts := natstest.DefaultTestOptions
	opts.Port = -1
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	subConn, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(subConn.Close)
	pubConn, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(pubConn.Close)

	sub := NewDisconnectCommandSubscriber(svc)
	require.NoError(t, sub.Start(subConn))
	t.Cleanup(sub.Stop)

	return &disconnectGateway{
		url:       "http://" + listener.Addr().String(),
		service:   svc,
		authority: authority,
		publisher: pubConn,
	}
}

// attach mints a credential bound to a fresh daemon id and opens that
// daemon's stream, returning the id and the stream once the gateway has
// acknowledged the registration.
func (g *disconnectGateway) attach(t *testing.T, userID string) (string, *connect.BidiStreamForClient[reliantv1.DaemonMessage, reliantv1.ServerMessage]) {
	t.Helper()
	daemonID := uuid.NewString()
	minted, err := g.authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID:   userID,
		Name:     "daemon " + daemonID,
		Scopes:   []fat.Scope{fat.ScopeDaemonConnect},
		Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: daemonID},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client := reliantv1connect.NewToolsDaemonServiceClient(h2cClient(), g.url, connect.WithGRPC())
	stream := client.ConnectDaemon(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+minted.Plaintext)
	t.Cleanup(func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	})
	require.NoError(t, stream.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Register{
		Register: &reliantv1.DaemonRegister{Hostname: "host-" + daemonID[:8], Platform: "linux", DaemonType: "local"},
	}}))
	for {
		msg, err := stream.Receive()
		require.NoError(t, err, "the stream ended before the registration ack")
		if ack := msg.GetRegistrationAck(); ack != nil {
			require.Equal(t, daemonID, ack.GetDaemonId(), "the bound credential names the daemon")
			return daemonID, stream
		}
	}
}

func (g *disconnectGateway) publish(t *testing.T, cmd daemonwire.DisconnectCommand) {
	t.Helper()
	body, err := json.Marshal(cmd)
	require.NoError(t, err)
	require.NoError(t, g.publisher.Publish(daemonwire.SubjectDisconnectCommand, body))
	require.NoError(t, g.publisher.Flush())
}

// streamEnd receives until the stream ends and returns how it ended, skipping
// the heartbeats and link probes the gateway sends meanwhile.
func streamEnd(t *testing.T, stream *connect.BidiStreamForClient[reliantv1.DaemonMessage, reliantv1.ServerMessage]) <-chan error {
	t.Helper()
	ended := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Receive(); err != nil {
				ended <- err
				return
			}
		}
	}()
	return ended
}

// The disconnect command, through the transport: an established, credential-
// authenticated daemon stream ends with Unauthenticated when the control
// plane publishes the command for its daemon; a command for a daemon nobody
// holds does nothing; and an unrelated daemon's stream, the same user's,
// stays open until something else ends it.
func TestDisconnectCommandEndsTheNamedDaemonsEstablishedStream(t *testing.T) {
	gw := startDisconnectGateway(t)
	revokedID, revoked := gw.attach(t, "user-1")
	otherID, other := gw.attach(t, "user-1")
	revokedEnd := streamEnd(t, revoked)
	otherEnd := streamEnd(t, other)

	// Unknown daemon: every replica hears it, none holds it.
	gw.publish(t, daemonwire.DisconnectCommand{DaemonID: uuid.NewString(), Reason: "credential revoked", RequestID: "r-unknown"})
	gw.publish(t, daemonwire.DisconnectCommand{DaemonID: revokedID, Reason: "credential revoked", RequestID: "r-1"})

	select {
	case err := <-revokedEnd:
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "got %v", err)
		require.Contains(t, err.Error(), "credential revoked")
	case <-time.After(10 * time.Second):
		t.Fatal("the revoked daemon's established stream did not end on the disconnect command")
	}
	require.Eventually(t, func() bool {
		_, _, held := gw.service.DaemonStatus(revokedID)
		return !held
	}, 5*time.Second, 10*time.Millisecond, "the revoked daemon must be unroutable")

	_, _, held := gw.service.DaemonStatus(otherID)
	require.True(t, held, "the unrelated daemon is still connected")
	select {
	case err := <-otherEnd:
		t.Fatalf("the unrelated daemon's stream ended: %v", err)
	default:
	}

	// The unrelated stream was open the whole time: ending the replica now
	// reaches it with the drain status, not the revocation one.
	gw.service.Close()
	select {
	case err := <-otherEnd:
		require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err),
			"the unrelated stream must end with the drain status, not the revocation; got %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the unrelated daemon's stream did not end on Close")
	}
}

// recordingDisconnecter records what the subscriber asks it to end.
type recordingDisconnecter struct{ calls [][2]string }

func (r *recordingDisconnecter) DisconnectDaemon(daemonID, reason string) bool {
	r.calls = append(r.calls, [2]string{daemonID, reason})
	return false
}

// A malformed command is dropped before it reaches the service; a valid one
// passes its daemon and reason through unchanged.
func TestDisconnectCommandSubscriberHandle(t *testing.T) {
	target := &recordingDisconnecter{}
	s := NewDisconnectCommandSubscriber(target)
	for _, payload := range []string{`{"reason":"credential revoked"}`, `not json`, `{"daemon_id":""}`} {
		s.handle(&nats.Msg{Subject: daemonwire.SubjectDisconnectCommand, Data: []byte(payload)})
	}
	require.Empty(t, target.calls, "a malformed command must not reach the service")

	s.handle(&nats.Msg{Subject: daemonwire.SubjectDisconnectCommand,
		Data: []byte(`{"daemon_id":"d-1","reason":"member removed from org","request_id":"r-1"}`)})
	require.Equal(t, [][2]string{{"d-1", "member removed from org"}}, target.calls)
}

// Stop is safe before Start and twice: the gateway's stop runs it whatever
// happened at boot.
func TestDisconnectCommandSubscriberStopIsIdempotent(t *testing.T) {
	s := NewDisconnectCommandSubscriber(&recordingDisconnecter{})
	s.Stop()
	s.Stop()
}

// chanDisconnecter hands each disconnect it is asked for to a channel.
type chanDisconnecter chan [2]string

func (c chanDisconnecter) DisconnectDaemon(daemonID, reason string) bool {
	c <- [2]string{daemonID, reason}
	return true
}

// reliant#2610: a gateway that subscribes before the broker has loaded the
// grant for the disconnect command is refused. It must re-subscribe once the
// grant lands, and a command published after that must end the stream.
func TestDisconnectCommandSubscriberRecoversFromARefusedSubscription(t *testing.T) {
	gatewayOpts := func(port int, subscribe ...string) *server.Options {
		opts := natstest.DefaultTestOptions
		opts.Port = port
		opts.Users = []*server.User{
			{Username: "daemon-gateway", Password: "pw", Permissions: &server.Permissions{
				Subscribe: &server.SubjectPermission{Allow: append([]string{"_INBOX.>"}, subscribe...)},
			}},
			{Username: "cp-core", Password: "pw"},
		}
		return &opts
	}
	srv := natstest.RunServer(gatewayOpts(-1)) // the grant has not rolled out yet
	t.Cleanup(srv.Shutdown)
	port := srv.Addr().(*net.TCPAddr).Port
	connect := func(user string) *nats.Conn {
		nc, err := natsutil.Connect(natsutil.Endpoint{URL: srv.ClientURL(), User: user, Password: "pw"})
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		return nc
	}
	gatewayConn, cpConn := connect("daemon-gateway"), connect("cp-core")

	events := make(chan natsutil.SubscriptionEvent, 64)
	await := func(kind natsutil.SubscriptionEventKind) {
		t.Helper()
		timeout := time.After(10 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.Kind == kind {
					return
				}
			case <-timeout:
				t.Fatalf("timed out waiting for the subscription to be %s", kind)
			}
		}
	}
	target := make(chanDisconnecter, 1)
	sub := NewDisconnectCommandSubscriber(target,
		natsutil.WithBackoff(10*time.Millisecond, 50*time.Millisecond),
		natsutil.WithEvents(func(ev natsutil.SubscriptionEvent) { events <- ev }))
	require.NoError(t, sub.Start(gatewayConn), "a refused subscription must not fail the gateway's start")
	t.Cleanup(sub.Stop)
	await(natsutil.SubscriptionRefused)

	require.NoError(t, srv.ReloadOptions(gatewayOpts(port, daemonwire.SubjectDisconnectCommand)))
	await(natsutil.SubscriptionRecovered)

	body, err := json.Marshal(daemonwire.DisconnectCommand{DaemonID: "d-1", Reason: "credential revoked", RequestID: "r-1"})
	require.NoError(t, err)
	require.NoError(t, cpConn.Publish(daemonwire.SubjectDisconnectCommand, body))
	select {
	case got := <-target:
		require.Equal(t, [2]string{"d-1", "credential revoked"}, got)
	case <-time.After(10 * time.Second):
		t.Fatal("a disconnect command published after the grant landed was not delivered")
	}
}
