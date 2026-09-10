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

// Neither resolution failure may name the account UUID. These messages are
// rendered verbatim to the end user (mapDaemonDispatchError wraps them into a
// Connect error the UI prints), and the owner hit
// "[internal] resolving daemon for command: no daemon available for user
// 22302879-fd98-4cde-9e12-532b12a5d3fc" in the product. The id belongs in the
// log, where an operator needs it — not in copy shown to the person it
// identifies.
func TestResolveDaemonID_ErrorsDoNotLeakTheUserID(t *testing.T) {
	const userID = "22302879-fd98-4cde-9e12-532b12a5d3fc"

	cases := []struct {
		name     string
		resp     *reliantv1.ResolveDaemonResponse
		selector *DaemonSelector
	}{
		{
			name: "no daemon at all",
			resp: &reliantv1.ResolveDaemonResponse{Found: false, Daemon: nil},
		},
		{
			name: "daemon record exists but is not routable",
			resp: &reliantv1.ResolveDaemonResponse{
				Found:  false,
				Daemon: &reliantv1.DaemonInfo{DaemonId: "daemon-provisioning"},
			},
		},
		{
			name:     "no daemon matching an explicit selector",
			resp:     &reliantv1.ResolveDaemonResponse{Found: false, Daemon: nil},
			selector: &DaemonSelector{Type: "managed", Name: "box", ID: "d-1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewNATSDaemonRouter(nil, WithControlPlaneClient(&fakeRegistryClient{resolveResp: tc.resp}))

			_, err := router.resolveDaemonID(context.Background(), userID, tc.selector)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), userID,
				"the account UUID must never appear in a message shown to the user")
			assert.Contains(t, err.Error(), "machine",
				"the message must talk about the user's machine, not about resolution plumbing")
		})
	}
}

// The pending path keeps the marker the frontend's wait machinery keys on,
// even though the message text around it changed. Losing this turns a
// "still starting, please wait" into a terminal error in the UI.
func TestResolveDaemonID_PendingKeepsTheConnectingMarker(t *testing.T) {
	router := NewNATSDaemonRouter(nil, WithControlPlaneClient(&fakeRegistryClient{
		resolveResp: &reliantv1.ResolveDaemonResponse{
			Found:  false,
			Daemon: &reliantv1.DaemonInfo{DaemonId: "daemon-provisioning"},
		},
	}))

	_, err := router.resolveDaemonID(context.Background(), "user-1", &DaemonSelector{Type: "managed"})
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err))
	assert.Contains(t, err.Error(), "no daemon connected",
		"isDaemonConnectingError keys on this marker")
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
