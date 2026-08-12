// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// stubRegistry is the control plane's side of
// reliant.v1.DaemonRegistryService, which is what it actually exposes for
// resume (translating onto its own DaemonService internally).
type stubRegistry struct {
	reliantv1connect.UnimplementedDaemonRegistryServiceHandler

	resumed   bool
	errMsg    string
	gotAuth   string
	gotID     string
	callCount int
}

func (s *stubRegistry) ResumeDaemon(
	_ context.Context,
	req *connect.Request[reliantv1.ResumeDaemonRequest],
) (*connect.Response[reliantv1.ResumeDaemonResponse], error) {
	s.callCount++
	s.gotAuth = req.Header().Get("Authorization")
	s.gotID = req.Msg.GetDaemonId()
	return connect.NewResponse(&reliantv1.ResumeDaemonResponse{
		Resumed:      s.resumed,
		ErrorMessage: s.errMsg,
	}), nil
}

// resumeRecorder serves a real Connect handler, so the client's wire format
// and headers are exercised rather than approximated.
func resumeRecorder(t *testing.T, resumed bool, errMsg string) (*httptest.Server, *stubRegistry) {
	t.Helper()
	stub := &stubRegistry{resumed: resumed, errMsg: errMsg}

	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewDaemonRegistryServiceHandler(stub))
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
