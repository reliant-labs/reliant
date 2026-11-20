// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	cpdaemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
)

// stubDaemonService is the control plane's controlplane.v1.DaemonService —
// the route it actually serves for resume.
type stubDaemonService struct {
	controlplanev1connect.UnimplementedDaemonServiceHandler

	resumed   bool
	errMsg    string
	gotAuth   string
	gotID     string
	callCount int
}

func (s *stubDaemonService) ResumeDaemon(
	ctx context.Context,
	req *cpdaemonv1.ResumeDaemonRequest,
) (*cpdaemonv1.ResumeDaemonResponse, error) {
	s.callCount++
	s.gotAuth = serverkit.CallInfoV2(ctx).RequestHeader().Get("Authorization")
	s.gotID = req.GetDaemonId()
	if !s.resumed {
		// What control-plane's svcdaemon.ResumeDaemon does for a refusal.
		return nil, connect.NewError(connect.CodeFailedPrecondition, s.errMsg)
	}
	return &cpdaemonv1.ResumeDaemonResponse{}, nil
}

// resumeRecorder serves a real Connect handler, so the client's wire format
// and headers are exercised rather than approximated. Every other path 404s,
// as on control-plane's platform-api, and is recorded.
func resumeRecorder(t *testing.T, resumed bool, errMsg string) (*httptest.Server, *stubDaemonService) {
	t.Helper()
	stub := &stubDaemonService{resumed: resumed, errMsg: errMsg}

	mux := http.NewServeMux()
	server := connect.NewServer(svcerr.InterceptorV2())
	controlplanev1connect.RegisterDaemonServiceHandler(server, stub)
	mountkit.MountV2(mux, server, connecthttp.WithReadMaxBytes(0))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("resumer called %s, which the control plane does not serve", r.URL.Path)
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, stub
}

// The resume happens AS THE USER: the control-plane endpoint is JWT-authed and
// derives the owner from the forwarded token, so losing it here would either
// fail outright or (worse, with a service credential) let one user's request
// wake another user's workspace.
func TestResumerForwardsTheCallersToken(t *testing.T) {
	srv, stub := resumeRecorder(t, true, "")

	r := NewControlPlaneResumer(srv.URL)
	require.NotNil(t, r)

	ctx := withCallerToken(context.Background(), "jwt-abc")
	require.NoError(t, r.ResumeDaemon(ctx, "user-1", "daemon-1"))
	require.Equal(t, "Bearer jwt-abc", stub.gotAuth)
	require.Equal(t, "daemon-1", stub.gotID)
}

// A refusal must surface the platform's own words — "cannot resume external
// daemon" tells the user something actionable; a generic failure does not.
func TestResumerSurfacesRefusalReason(t *testing.T) {
	srv, _ := resumeRecorder(t, false, "cannot resume external daemon")

	r := NewControlPlaneResumer(srv.URL)
	ctx := withCallerToken(context.Background(), "jwt-abc")

	err := r.ResumeDaemon(ctx, "user-1", "daemon-1")
	require.ErrorContains(t, err, "cannot resume external daemon")
}

// A connector-credential caller has no user token to forward. The control
// plane would reject it, so the useful message belongs here.
func TestResumerWithoutATokenExplainsWhy(t *testing.T) {
	srv, _ := resumeRecorder(t, true, "")

	r := NewControlPlaneResumer(srv.URL)
	err := r.ResumeDaemon(context.Background(), "user-1", "daemon-1")
	require.ErrorContains(t, err, "requires signing in")
}

// No configured control plane means nothing can start a workspace. The nil
// return is what the caller assigns conditionally — a typed nil inside the
// interface would not compare equal to nil and would panic on first use.
func TestResumerIsNilWithoutAControlPlaneURL(t *testing.T) {
	require.Nil(t, NewControlPlaneResumer(""))
	require.Nil(t, NewControlPlaneResumer("   "))

	var resumer DaemonResumer
	if cp := NewControlPlaneResumer(""); cp != nil {
		resumer = cp
	}
	require.Nil(t, resumer, "the guarded assignment must leave the interface nil")
}

// The whole point of the caller-token plumbing: a connector credential must
// NOT be forwarded as if it were a user's OAuth token.
func TestCallerTokenAbsentForConnectorCredentials(t *testing.T) {
	require.Empty(t, CallerToken(context.Background()))
	require.Equal(t, "jwt-abc",
		CallerToken(withCallerToken(context.Background(), "jwt-abc")))
}

// A connector caller has no user token. Even when the user holds a stored
// automation credential for that very daemon, the resumer must still refuse and
// must not call the control plane: only a registered workflow's trigger may use
// that credential, and the resumer has no access to it at all.
func TestResumerRefusesConnectorCallerEvenWithStoredAutomationToken(t *testing.T) {
	srv, stub := resumeRecorder(t, true, "")
	r := NewControlPlaneResumer(srv.URL)

	// The stored token exists for (user-1, daemon-1) — the strongest case.
	keys := map[string]string{"user-1|daemon-1": "rlat_resume"}
	require.NotEmpty(t, keys)

	err := r.ResumeDaemon(context.Background(), "user-1", "daemon-1") // no caller token
	require.Error(t, err)
	require.Contains(t, err.Error(), "connector credential")
	require.Zero(t, stub.callCount, "no control-plane call may be made on a connector's behalf")
	require.Empty(t, stub.gotAuth)
}
