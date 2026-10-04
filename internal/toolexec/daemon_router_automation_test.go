// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
)

type credFunc func(ctx context.Context, userID, daemonID string) (string, error)

func (f credFunc) BearerFor(ctx context.Context, userID, daemonID string) (string, error) {
	return f(ctx, userID, daemonID)
}

// bindingRegistry is a fake control plane that enforces what the real one does:
// a daemon-bound token works only for its daemon and only when daemon_id is sent.
type bindingRegistry struct {
	reliantv1connect.UnimplementedDaemonRegistryServiceHandler
	mu        sync.Mutex
	token     string
	daemonID  string
	suspended bool
	resolves  []string // Authorization headers
	resumes   []string
	resumeIDs []string
	resolveID []string
}

func (b *bindingRegistry) check(h http.Header, id string) error {
	if h.Get("Authorization") != "Bearer "+b.token {
		return connect.NewError(connect.CodeUnauthenticated, nil)
	}
	if id == "" {
		return connect.NewError(connect.CodeInvalidArgument, nil)
	}
	if id != b.daemonID {
		return connect.NewError(connect.CodePermissionDenied, nil)
	}
	return nil
}

func (b *bindingRegistry) ResolveDaemon(_ context.Context, req *connect.Request[reliantv1.ResolveDaemonRequest]) (*connect.Response[reliantv1.ResolveDaemonResponse], error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resolves = append(b.resolves, req.Header().Get("Authorization"))
	b.resolveID = append(b.resolveID, req.Msg.GetDaemonId())
	if err := b.check(req.Header(), req.Msg.GetDaemonId()); err != nil {
		return nil, err
	}
	status := reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE
	if b.suspended {
		status = reliantv1.DaemonStatus_DAEMON_STATUS_IDLE
	}
	return connect.NewResponse(&reliantv1.ResolveDaemonResponse{
		Found: true, Daemon: &reliantv1.DaemonInfo{DaemonId: b.daemonID, Status: status}}), nil
}

func (b *bindingRegistry) ResumeDaemon(_ context.Context, req *connect.Request[reliantv1.ResumeDaemonRequest]) (*connect.Response[reliantv1.ResumeDaemonResponse], error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resumes = append(b.resumes, req.Header().Get("Authorization"))
	b.resumeIDs = append(b.resumeIDs, req.Msg.GetDaemonId())
	if err := b.check(req.Header(), req.Msg.GetDaemonId()); err != nil {
		return nil, err
	}
	b.suspended = false
	return connect.NewResponse(&reliantv1.ResumeDaemonResponse{Resumed: true}), nil
}

func newBindingRouter(t *testing.T, reg *bindingRegistry, creds ControlPlaneCredentials) *NATSDaemonRouter {
	t.Helper()
	path, h := reliantv1connect.NewDaemonRegistryServiceHandler(reg)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := reliantv1connect.NewDaemonRegistryServiceClient(http.DefaultClient, srv.URL)
	return NewNATSDaemonRouter(nil, WithControlPlaneClient(client), WithControlPlaneCredentials(creds))
}

func TestRouterUsesAutomationTokenAndPinnedDaemonOnResolveAndResume(t *testing.T) {
	reg := &bindingRegistry{token: "rlat_a", daemonID: "daemon-a", suspended: true}
	router := newBindingRouter(t, reg, credFunc(func(_ context.Context, _, daemonID string) (string, error) {
		if daemonID == "daemon-a" {
			return "rlat_a", nil
		}
		return "", nil
	}))

	id, err := router.EnsureAwake(context.Background(), "user-x", &DaemonSelector{ID: "daemon-a"})
	require.NoError(t, err)
	assert.Equal(t, "daemon-a", id)
	assert.Equal(t, []string{"Bearer rlat_a"}, reg.resolves)
	assert.Equal(t, []string{"daemon-a"}, reg.resolveID, "ResolveDaemon must carry the pinned daemon_id")
	assert.Equal(t, []string{"Bearer rlat_a"}, reg.resumes, "ResumeDaemon must carry the same Bearer")
	assert.Equal(t, []string{"daemon-a"}, reg.resumeIDs, "exactly one resume, for the pinned daemon")
}

func TestRouterRefusesDaemonTheTokenIsNotBoundTo(t *testing.T) {
	reg := &bindingRegistry{token: "rlat_a", daemonID: "daemon-a", suspended: true}
	// A (buggy) credential source that hands daemon-a's token for daemon-b.
	router := newBindingRouter(t, reg, credFunc(func(context.Context, string, string) (string, error) { return "rlat_a", nil }))

	_, err := router.EnsureAwake(context.Background(), "user-x", &DaemonSelector{ID: "daemon-b"})
	require.Error(t, err)
	assert.Empty(t, reg.resumes)
}

func TestRouterWithoutAnyCredentialReportsAutomationNotGranted(t *testing.T) {
	reg := &bindingRegistry{token: "rlat_a", daemonID: "daemon-a"}
	router := newBindingRouter(t, reg, credFunc(func(context.Context, string, string) (string, error) { return "", nil }))

	_, err := router.EnsureAwake(context.Background(), "user-x", &DaemonSelector{ID: "daemon-a"})
	require.ErrorIs(t, err, ErrAutomationAccessNotGranted)
	assert.Empty(t, reg.resolves, "no request is sent without a credential")

	// Tool-time resolution of a pinned daemon needs no credential and sends no
	// control-plane request; NATS decides whether the daemon is reachable.
	id, err := router.resolveDaemonID(context.Background(), "user-x", &DaemonSelector{ID: "daemon-a"})
	require.NoError(t, err)
	assert.Equal(t, "daemon-a", id)
	assert.Empty(t, reg.resolves)
	assert.Empty(t, reg.resumes)
}

func TestRouterWithoutCredentialsOptionKeepsUsingUserJWT(t *testing.T) {
	auth.SetUserJWT("user-jwt-only", "jwt-1")
	t.Cleanup(func() { auth.SetUserJWT("user-jwt-only", "") })
	reg := &bindingRegistry{token: "jwt-1", daemonID: "daemon-a"}
	path, h := reliantv1connect.NewDaemonRegistryServiceHandler(reg)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	router := NewNATSDaemonRouter(nil, WithControlPlaneClient(reliantv1connect.NewDaemonRegistryServiceClient(http.DefaultClient, srv.URL)))

	id, err := router.EnsureAwake(context.Background(), "user-jwt-only", &DaemonSelector{ID: "daemon-a"})
	require.NoError(t, err)
	assert.Equal(t, "daemon-a", id)
}
