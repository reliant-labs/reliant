// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// ownershipRepo is an in-memory daemons table with the same immutable-owner
// contract as the real UpsertDaemon. Embedding the interface means any method
// the registration path should NOT reach panics instead of silently passing.
type ownershipRepo struct {
	db.Repository
	mu     sync.Mutex
	owners map[string]string
}

func newOwnershipRepo(owners map[string]string) *ownershipRepo {
	return &ownershipRepo{owners: owners}
}

func (r *ownershipRepo) ownerOf(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owners[id]
}

func (r *ownershipRepo) ListProjects(context.Context, db.ProjectFilters) ([]*db.Project, error) {
	return nil, nil
}

func (r *ownershipRepo) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, ok := r.owners[id]; ok {
		return &db.Daemon{ID: id, UserID: u}, nil
	}
	return nil, fmt.Errorf("not found")
}

func (r *ownershipRepo) UpsertDaemon(_ context.Context, d *db.Daemon) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, ok := r.owners[d.ID]; ok && u != d.UserID {
		return db.ErrDaemonOwnedByAnotherUser
	}
	r.owners[d.ID] = d.UserID
	return nil
}

func (r *ownershipRepo) UpsertDaemonAttachment(context.Context, *db.DaemonAttachment) error {
	return nil
}

// recordingListener counts connect notifications, i.e. NATS side effects.
type recordingListener struct {
	mu        sync.Mutex
	connected []string
}

func (l *recordingListener) OnDaemonConnected(userID, daemonID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.connected = append(l.connected, userID+"/"+daemonID)
}
func (l *recordingListener) OnDaemonDisconnected(string, string) {}

// registerAs dials ConnectDaemon authenticated (by test header) as userID,
// asserting assertedID, and returns the first thing the gateway answers.
func registerAs(t *testing.T, svc *ToolsDaemonService, userID, assertedID string) error {
	t.Helper()
	path, h := reliantv1connect.NewToolsDaemonServiceHandler(svc)
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.UserIDContextKey, r.Header.Get("X-Test-User"))
		h.ServeHTTP(w, r.WithContext(ctx))
	})
	mux := http.NewServeMux()
	mux.Handle(path, wrapped)
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.Start()
	defer srv.Close()

	cl := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := reliantv1connect.NewToolsDaemonServiceClient(cl, srv.URL, connect.WithGRPC())
	stream := client.ConnectDaemon(ctx)
	stream.RequestHeader().Set("X-Test-User", userID)
	defer func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() }()
	_ = stream.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Register{
		Register: &reliantv1.DaemonRegister{DaemonId: assertedID, Hostname: "h", Platform: "linux", DaemonType: "local"},
	}})
	_, err := stream.Receive()
	return err
}

func TestConnectDaemonRefusesDaemonIDOwnedByAnotherUser(t *testing.T) {
	daemonID := uuid.NewString()
	repo := newOwnershipRepo(map[string]string{daemonID: "user-A"})
	svc := NewToolsDaemonService(repo)
	t.Cleanup(svc.Close)
	listener := &recordingListener{}
	svc.AddConnectionListener(listener)

	victim := newTestConn("user-A", daemonID, newParkedStream())
	svc.mu.Lock()
	svc.connections[daemonID] = victim
	svc.userDaemons["user-A"] = []string{daemonID}
	svc.mu.Unlock()

	err := registerAs(t, svc, "user-B", daemonID)

	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Equal(t, "user-A", repo.ownerOf(daemonID), "row must still belong to A")
	require.NoError(t, victim.closedReason(), "A's connection must not be superseded")
	svc.mu.RLock()
	require.Same(t, victim, svc.connections[daemonID])
	svc.mu.RUnlock()
	listener.mu.Lock()
	require.Empty(t, listener.connected, "no NATS-side notification for a refused registration")
	listener.mu.Unlock()
	require.Empty(t, svc.userDaemons["user-B"])
}

func TestConnectDaemonRefusesSupersedingAnotherUsersLiveConnection(t *testing.T) {
	// Defense in depth: even if the row check were bypassed (row absent), a
	// live incumbent owned by someone else must not be superseded.
	daemonID := uuid.NewString()
	repo := newOwnershipRepo(map[string]string{})
	svc := NewToolsDaemonService(repo)
	t.Cleanup(svc.Close)
	victim := newTestConn("user-A", daemonID, newParkedStream())
	svc.mu.Lock()
	svc.connections[daemonID] = victim
	svc.mu.Unlock()

	err := registerAs(t, svc, "user-B", daemonID)

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.NoError(t, victim.closedReason())
}

func TestValidateAssertedDaemonID(t *testing.T) {
	for _, ok := range []string{"", "  ", uuid.NewString(), "abc-123", "A"} {
		require.NoError(t, validateAssertedDaemonID(ok), "%q", ok)
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	for _, bad := range []string{"a.b", "a>b", "a*b", "a b\tc", "a_b", "../x", "a\x00b", string(long), "é"} {
		err := validateAssertedDaemonID(bad)
		require.Error(t, err, "%q", bad)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
}

func TestConnectDaemonRejectsMalformedAssertedID(t *testing.T) {
	svc := NewToolsDaemonService(newOwnershipRepo(map[string]string{}))
	t.Cleanup(svc.Close)
	err := registerAs(t, svc, "user-B", "evil.>")
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
