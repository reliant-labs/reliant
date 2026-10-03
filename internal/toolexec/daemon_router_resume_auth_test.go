// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// authRecordingRegistryClient records the Authorization header of every
// control-plane call, and reports the resolved daemon as suspended so the
// router takes its resume branch.
type authRecordingRegistryClient struct {
	fakeRegistryClient
	resolveAuth string
	resumeAuth  string
	resumed     bool
}

func (f *authRecordingRegistryClient) ResolveDaemon(_ context.Context, req *connect.Request[reliantv1.ResolveDaemonRequest]) (*connect.Response[reliantv1.ResolveDaemonResponse], error) {
	f.resolveAuth = req.Header().Get("Authorization")
	return connect.NewResponse(&reliantv1.ResolveDaemonResponse{
		Found: true,
		Daemon: &reliantv1.DaemonInfo{
			DaemonId: "daemon-suspended",
			Status:   reliantv1.DaemonStatus_DAEMON_STATUS_IDLE,
		},
	}), nil
}

func (f *authRecordingRegistryClient) ResumeDaemon(_ context.Context, req *connect.Request[reliantv1.ResumeDaemonRequest]) (*connect.Response[reliantv1.ResumeDaemonResponse], error) {
	f.resumeAuth = req.Header().Get("Authorization")
	f.resumed = true
	return connect.NewResponse(&reliantv1.ResumeDaemonResponse{Resumed: true}), nil
}

// TestResolveViaControlPlane_ResumeCarriesTheCallersBearer: waking a suspended
// daemon must authenticate as the user, exactly like resolving it does.
//
// The control plane's DaemonRegistryService adapter has no service-credential
// path — its ResumeDaemon handler reads the owner from the forwarded Bearer
// (svcdaemon.ResumeDaemon → auth.GetOwner) and rejects a call without one. The
// resolve call already attached the user's JWT; the resume call that follows
// it did not, so every automatic wake from the router was rejected as
// unauthenticated and surfaced as "daemon could not be resumed" — the router's
// whole suspended-daemon branch could never succeed.
func TestResolveViaControlPlane_ResumeCarriesTheCallersBearer(t *testing.T) {
	const userID = "user-resume-auth"
	auth.SetUserJWT(userID, "jwt-for-resume")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })

	registry := &authRecordingRegistryClient{}
	router := NewNATSDaemonRouter(nil, WithControlPlaneClient(registry))

	id, sawRecord, err := router.resolveViaControlPlane(context.Background(), userID, nil)
	require.NoError(t, err)
	assert.True(t, sawRecord)
	assert.Equal(t, "daemon-suspended", id)

	require.True(t, registry.resumed, "a suspended daemon must be resumed")
	assert.Equal(t, "Bearer jwt-for-resume", registry.resolveAuth)
	assert.Equal(t, "Bearer jwt-for-resume", registry.resumeAuth,
		"ResumeDaemon must carry the same Bearer as ResolveDaemon; the control plane derives the owner from it")
}
