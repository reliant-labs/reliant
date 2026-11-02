// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
)

// A daemon id that a daemon-bound credential has connected as — or that the
// gateway dialed in for the control plane — is reserved: from then on an
// unbound login token of the SAME user cannot connect as it, so a laptop's
// daemon cannot take over the user's cloud machine's identity or supersede its
// live connection. These run the real ConnectDaemon path against the real
// UpsertDaemon SQL.

// connectAs dials ConnectDaemon as userID, with a credential bound to boundID
// ("" for an unbound login token), asserting assertedID. It returns the stream
// once the gateway acknowledged the registration, or the refusal. An accepted
// stream stays open until the test ends.
func connectAs(t *testing.T, svc *Service, userID, boundID, assertedID string) (*connect.BidiStreamForClient[reliantv1.DaemonMessage, reliantv1.ServerMessage], error) {
	t.Helper()
	routes := http.NewServeMux()
	svc.Register(routes, connect.WithInterceptors(svcerr.Interceptor()))
	mux := serverkit.NewStreams().Install(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.UserIDContextKey, r.Header.Get("X-Test-User"))
		if bound := r.Header.Get("X-Test-Bound-Daemon"); bound != "" {
			// What the daemon auth interceptor puts on ctx for a daemon-bound rlat_.
			ctx = context.WithValue(ctx, auth.DaemonIDContextKey, bound)
		}
		routes.ServeHTTP(w, r.WithContext(ctx))
	}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.Start()
	t.Cleanup(srv.Close)

	cl := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream := reliantv1connect.NewToolsDaemonServiceClient(cl, srv.URL, connect.WithGRPC()).ConnectDaemon(ctx)
	stream.RequestHeader().Set("X-Test-User", userID)
	if boundID != "" {
		stream.RequestHeader().Set("X-Test-Bound-Daemon", boundID)
	}
	t.Cleanup(func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() })
	require.NoError(t, stream.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Register{
		Register: &reliantv1.DaemonRegister{DaemonId: assertedID, Hostname: "h-" + userID, Platform: "linux", DaemonType: "local"},
	}}))
	msg, err := stream.Receive()
	if err != nil {
		return nil, err
	}
	require.NotNil(t, msg.GetRegistrationAck(), "first server message must be the registration ack")
	return stream, nil
}

func requireReserved(t *testing.T, repo *db.Repo, daemonID string, want bool) {
	t.Helper()
	d, err := repo.GetDaemon(context.Background(), daemonID)
	require.NoError(t, err)
	require.Equal(t, want, d.RequiresBoundToken, "daemon %s requires_bound_token", daemonID)
}

func requireReservedRefusal(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
	require.True(t, bootstrap.IsReservedDaemonIDError(err),
		"the refusal must carry the reserved-id reason, so the daemon never reads it as a bad credential: %v", err)
	require.False(t, bootstrap.IsForeignDaemonIDError(err), "the caller owns this daemon; it is not foreign")
}

// A bound credential connecting reserves its id, and keeps working on every
// reconnect.
func TestConnectDaemonBoundCredentialReservesItsID(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	cloud := uuid.NewString()

	_, err := connectAs(t, svc, "user-A", cloud, "")
	require.NoError(t, err)
	requireReserved(t, repo, cloud, true)

	// A reconnect with the same bound credential (asserting its id, as a
	// daemon re-asserts its saved id) is accepted and supersedes its own
	// earlier stream.
	_, err = connectAs(t, svc, "user-A", cloud, cloud)
	require.NoError(t, err)
	requireReserved(t, repo, cloud, true)
}

// THE fix. The same user's login token asserting the reserved id is refused
// before any side effect: the live bound connection is not superseded, no
// connect notification fires, and the row keeps its registration.
func TestConnectDaemonRefusesUnboundTokenOnReservedID(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	cloud := uuid.NewString()

	_, err := connectAs(t, svc, "user-A", cloud, "")
	require.NoError(t, err)
	incumbent := heldConn(svc, cloud)
	require.NotNil(t, incumbent)

	listener := &recordingListener{}
	svc.AddConnectionListener(listener)

	_, err = connectAs(t, svc, "user-A", "", cloud)

	requireReservedRefusal(t, err)
	require.Same(t, incumbent, heldConn(svc, cloud), "the cloud machine's live connection must not be superseded")
	require.NoError(t, incumbent.closedReason())
	listener.mu.Lock()
	require.Empty(t, listener.connected, "no connect notification for a refused registration")
	listener.mu.Unlock()
	d, err := repo.GetDaemon(context.Background(), cloud)
	require.NoError(t, err)
	require.Equal(t, "h-user-A", *d.Hostname)
	require.True(t, d.RequiresBoundToken, "a refused unbound registration must not clear the reservation")
}

// The refusal holds when the reserved machine is not connected at all: the
// row, not the live connection, is what reserves the id (every replica sees it).
func TestConnectDaemonRefusesUnboundTokenOnReservedIDWithNoLiveConnection(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	cloud := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{ID: cloud, UserID: "user-A", RequiresBoundToken: true}))
	svc := newTestService(repo)
	t.Cleanup(svc.Close)

	_, err := connectAs(t, svc, "user-A", "", cloud)

	requireReservedRefusal(t, err)
	require.Nil(t, heldConn(svc, cloud))
}

// Defense in depth: even with the reserved row gone, an unbound credential
// may not supersede a live bound connection on this replica.
func TestConnectDaemonUnboundTokenCannotSupersedeLiveBoundConnection(t *testing.T) {
	svc := newTestService(newOwnershipRepo(map[string]string{}))
	t.Cleanup(svc.Close)
	cloud := uuid.NewString()
	incumbent := newTestConn("user-A", cloud, newParkedStream())
	incumbent.boundCredential = true
	svc.mu.Lock()
	svc.connections[cloud] = incumbent
	svc.userDaemons["user-A"] = []string{cloud}
	svc.mu.Unlock()

	_, err := connectAs(t, svc, "user-A", "", cloud)

	requireReservedRefusal(t, err)
	require.Same(t, incumbent, heldConn(svc, cloud))
	require.NoError(t, incumbent.closedReason())
}

// Laptops are untouched: a login token registers, reconnects as its saved
// id, and never reserves it.
func TestConnectDaemonUnboundTokenKeepsWorkingOnUnreservedID(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	laptop := uuid.NewString()

	_, err := connectAs(t, svc, "user-A", "", laptop)
	require.NoError(t, err)
	requireReserved(t, repo, laptop, false)

	_, err = connectAs(t, svc, "user-A", "", laptop)
	require.NoError(t, err, "an unbound reconnect as an unreserved id is accepted")
	requireReserved(t, repo, laptop, false)
}

// A daemon that registered with a login token and later connects with a
// daemon token for its id becomes reserved from that connect on.
func TestConnectDaemonBoundConnectReservesAnUnboundRow(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	machine := uuid.NewString()

	_, err := connectAs(t, svc, "user-A", "", machine)
	require.NoError(t, err)
	requireReserved(t, repo, machine, false)

	_, err = connectAs(t, svc, "user-A", machine, machine)
	require.NoError(t, err)
	requireReserved(t, repo, machine, true)

	_, err = connectAs(t, svc, "user-A", "", machine)
	requireReservedRefusal(t, err)
}

// The dial-in path: the gateway dials a managed pod the control plane named.
// That identity is as authoritative as a bound credential, so it reserves the
// id, and a login token then cannot connect as the pod or supersede the
// gateway's live outbound connection to it.
func TestRegisterOutboundConnectionReservesIDAgainstUnboundTokens(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	pod := uuid.NewString()

	out := dialInPod(t, svc, "user-A", pod)
	t.Cleanup(out.Disconnect)
	requireReserved(t, repo, pod, true)
	incumbent := heldConn(svc, pod)
	require.NotNil(t, incumbent)

	_, err := connectAs(t, svc, "user-A", "", pod)

	requireReservedRefusal(t, err)
	require.Same(t, incumbent, heldConn(svc, pod), "the gateway's outbound connection must not be superseded")
	require.NoError(t, incumbent.closedReason())
}

// The dial-in path reserves an id first registered by a login token too, and
// is never itself refused by a reservation: it is the authoritative identity.
func TestRegisterOutboundConnectionReservesAnUnboundRow(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	pod := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{ID: pod, UserID: "user-A"}))
	svc := newTestService(repo)
	t.Cleanup(svc.Close)

	out := dialInPod(t, svc, "user-A", pod)
	t.Cleanup(out.Disconnect)
	requireReserved(t, repo, pod, true)
}

// parkedPodHandler is a managed pod's ConnectGateway: it accepts the stream
// and holds it open until the gateway closes it.
type parkedPodHandler struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
}

func (parkedPodHandler) ConnectGateway(ctx context.Context, _ *connect.BidiStream[reliantv1.ServerMessage, reliantv1.DaemonMessage]) error {
	<-ctx.Done()
	return nil
}

// dialInPod registers daemonID as userID through RegisterOutboundConnection,
// over a real ConnectGateway stream to a parked pod, the way the gateway's
// DaemonConnector does after the pod's DaemonRegister arrives.
func dialInPod(t *testing.T, svc *Service, userID, daemonID string) *OutboundConn {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewToolsDaemonServiceHandler(parkedPodHandler{}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.Start()
	t.Cleanup(srv.Close)
	cl := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream := reliantv1connect.NewToolsDaemonServiceClient(cl, srv.URL, connect.WithGRPC()).ConnectGateway(ctx)
	require.NoError(t, stream.Send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_Hello{Hello: &reliantv1.GatewayHello{}}}))
	t.Cleanup(func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() })

	regCtx, regCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer regCancel()
	out, err := svc.RegisterOutboundConnection(regCtx, userID, daemonID, "10.0.0.1", 9190,
		&reliantv1.DaemonRegister{Hostname: "ws-pod", Platform: "linux", DaemonType: "cloud"}, stream, cancel)
	require.NoError(t, err)
	return out
}

// An unbound daemon that asserts no id reuses its own row by hostname — but
// never a reserved one, which it could only be refused.
func TestResolveUnboundDaemonIDSkipsReservedRows(t *testing.T) {
	host := "shared-host"
	repo := hostnameRepo{daemons: []*db.Daemon{
		{ID: "cloud", UserID: "user-A", Hostname: &host, RequiresBoundToken: true},
	}}
	got := resolveUnboundDaemonID(context.Background(), repo, "user-A", host)
	require.NotEqual(t, "cloud", got, "a reserved row is never reused by hostname")

	repo.daemons = append(repo.daemons, &db.Daemon{ID: "laptop", UserID: "user-A", Hostname: &host})
	require.Equal(t, "laptop", resolveUnboundDaemonID(context.Background(), repo, "user-A", host))
}

type hostnameRepo struct{ daemons []*db.Daemon }

func (r hostnameRepo) ListDaemonsByUserID(context.Context, string) ([]*db.Daemon, error) {
	return r.daemons, nil
}
