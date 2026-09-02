// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRegistryClient is a minimal reliantv1connect.DaemonRegistryServiceClient
// stub so resolveDaemonID's control-plane branch can be exercised without a
// running control plane.
type fakeRegistryClient struct {
	resolveResp *reliantv1.ResolveDaemonResponse
	resolveErr  error
}

func (f *fakeRegistryClient) ListDaemons(context.Context, *connect.Request[reliantv1.ListDaemonsRequest]) (*connect.Response[reliantv1.ListDaemonsResponse], error) {
	return nil, errors.New("not implemented")
}

func (f *fakeRegistryClient) GetDaemon(context.Context, *connect.Request[reliantv1.GetDaemonRequest]) (*connect.Response[reliantv1.GetDaemonResponse], error) {
	return nil, errors.New("not implemented")
}

func (f *fakeRegistryClient) ResolveDaemon(context.Context, *connect.Request[reliantv1.ResolveDaemonRequest]) (*connect.Response[reliantv1.ResolveDaemonResponse], error) {
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	return connect.NewResponse(f.resolveResp), nil
}

func (f *fakeRegistryClient) ResumeDaemon(context.Context, *connect.Request[reliantv1.ResumeDaemonRequest]) (*connect.Response[reliantv1.ResumeDaemonResponse], error) {
	return nil, errors.New("not implemented")
}

var _ reliantv1connect.DaemonRegistryServiceClient = (*fakeRegistryClient)(nil)

// (a) A daemon record exists (control plane returns Found=false but names a
// DaemonId — the ResolveDaemonEndpoint shape for "provisioning, no endpoint
// yet") must resolve to ErrDaemonPending, not a flat "no daemon" error.
func TestResolveDaemonID_DaemonExistsButNotConnected_ReturnsPendingSignal(t *testing.T) {
	router := NewNATSDaemonRouter(nil, WithControlPlaneClient(&fakeRegistryClient{
		resolveResp: &reliantv1.ResolveDaemonResponse{
			Found:  false,
			Daemon: &reliantv1.DaemonInfo{DaemonId: "daemon-provisioning"},
		},
	}))

	_, err := router.resolveDaemonID(context.Background(), "user-1", nil)
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err), "expected ErrDaemonPending, got: %v", err)
	assert.Contains(t, err.Error(), "no daemon connected",
		"message must carry the marker the frontend's isDaemonConnectingError keys on")
}

// (b) When no daemon record exists at all (control plane reports Found=false
// with no Daemon), the router must still return a hard, non-pending error —
// the genuine-error path must not be softened into an infinite wait.
func TestResolveDaemonID_NoDaemonAtAll_ReturnsHardError(t *testing.T) {
	router := NewNATSDaemonRouter(nil, WithControlPlaneClient(&fakeRegistryClient{
		resolveResp: &reliantv1.ResolveDaemonResponse{Found: false, Daemon: nil},
	}))

	_, err := router.resolveDaemonID(context.Background(), "user-1", nil)
	require.Error(t, err)
	assert.False(t, IsDaemonPending(err), "user with truly no daemon must get a real error, not the pending signal")
	assert.Contains(t, err.Error(), "no daemon available")
}

// A daemon that resolves cleanly (control plane finds it and it's routable)
// must return the id with no error, pending or otherwise.
func TestResolveDaemonID_DaemonResolves_ReturnsID(t *testing.T) {
	router := NewNATSDaemonRouter(nil, WithControlPlaneClient(&fakeRegistryClient{
		resolveResp: &reliantv1.ResolveDaemonResponse{
			Found:  true,
			Daemon: &reliantv1.DaemonInfo{DaemonId: "daemon-active", Status: reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE},
		},
	}))

	id, err := router.resolveDaemonID(context.Background(), "user-1", nil)
	require.NoError(t, err)
	assert.Equal(t, "daemon-active", id)
}
