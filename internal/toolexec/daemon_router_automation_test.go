// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

type credFunc func(ctx context.Context, userID, daemonID string) (string, error)

func (f credFunc) BearerFor(ctx context.Context, userID, daemonID string) (string, error) {
	return f(ctx, userID, daemonID)
}

// newBindingRouter: the registry records the given daemons as suspended for
// user-x, and the resumer enforces daemon:resume binding like control-plane.
func newBindingRouter(resumer *fakeResumer, creds ControlPlaneCredentials, daemonIDs ...string) *NATSDaemonRouter {
	records := &fakeDaemonRecords{}
	for _, id := range daemonIDs {
		records.daemons = append(records.daemons, &db.Daemon{ID: id, UserID: "user-x", LifecyclePhase: strPtr("suspended")})
	}
	return NewNATSDaemonRouter(nil, WithDatabase(records), WithDaemonResumer(resumer), WithControlPlaneCredentials(creds))
}

func boundTo(daemonID string) credFunc {
	return func(_ context.Context, _, requested string) (string, error) {
		if requested == daemonID {
			return "rlat_" + daemonID, nil
		}
		return "", nil
	}
}

func TestRouterUsesAutomationTokenForThePinnedDaemonOnResume(t *testing.T) {
	resumer := &fakeResumer{boundTokens: map[string]string{"daemon-a": "rlat_daemon-a"}}
	router := newBindingRouter(resumer, boundTo("daemon-a"), "daemon-a")

	id, err := router.EnsureAwake(context.Background(), "user-x", &DaemonSelector{ID: "daemon-a"})
	require.NoError(t, err)
	assert.Equal(t, "daemon-a", id)
	assert.Equal(t, []resumeCall{{token: "rlat_daemon-a", daemonID: "daemon-a"}}, resumer.calls,
		"exactly one resume, for the pinned daemon, with its bound token")
}

func TestRouterRefusesDaemonTheTokenIsNotBoundTo(t *testing.T) {
	resumer := &fakeResumer{boundTokens: map[string]string{"daemon-a": "rlat_daemon-a"}}
	// A (buggy) credential source that hands daemon-a's token for daemon-b.
	router := newBindingRouter(resumer, credFunc(func(context.Context, string, string) (string, error) {
		return "rlat_daemon-a", nil
	}), "daemon-b")

	_, err := router.EnsureAwake(context.Background(), "user-x", &DaemonSelector{ID: "daemon-b"})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestRouterWithoutAnyCredentialReportsAutomationNotGranted(t *testing.T) {
	resumer := &fakeResumer{}
	router := newBindingRouter(resumer, credFunc(func(context.Context, string, string) (string, error) { return "", nil }), "daemon-a")

	_, err := router.EnsureAwake(context.Background(), "user-x", &DaemonSelector{ID: "daemon-a"})
	require.ErrorIs(t, err, ErrAutomationAccessNotGranted)
	assert.Empty(t, resumer.calls, "no resume is sent without a credential")

	// Tool-time resolution needs no credential at all — it reads the
	// registry — and reports the parked daemon as pending, never waking it.
	_, err = router.resolveDaemonID(context.Background(), "user-x", &DaemonSelector{ID: "daemon-a"})
	assert.True(t, IsDaemonPending(err), "got: %v", err)
	assert.Empty(t, resumer.calls)
}

// The delegated token is asked for by PINNED daemon only. An unattended run
// with no selector must not wake whichever daemon default resolution picked
// with a token bound to it — the trigger names exactly one daemon.
func TestRouterNeverAsksForADelegatedTokenForAnUnpinnedDaemon(t *testing.T) {
	var asked []string
	resumer := &fakeResumer{}
	router := newBindingRouter(resumer, credFunc(func(_ context.Context, _, daemonID string) (string, error) {
		asked = append(asked, daemonID)
		return boundTo("daemon-a")(context.Background(), "", daemonID)
	}), "daemon-a")

	_, err := router.EnsureAwake(context.Background(), "user-x", nil)
	require.ErrorIs(t, err, ErrAutomationAccessNotGranted)
	assert.Equal(t, []string{""}, asked, "the credential source is asked for the pinned id, which is empty")
	assert.Empty(t, resumer.calls)
}

// An attached daemon needs no wake, so EnsureAwake needs no credential for it.
func TestRouterNeedsNoCredentialForAnAttachedDaemon(t *testing.T) {
	resumer := &fakeResumer{}
	router := NewNATSDaemonRouter(nil, WithDaemonResumer(resumer),
		WithControlPlaneCredentials(credFunc(func(context.Context, string, string) (string, error) { return "", nil })),
		WithDatabase(&fakeDaemonRecords{
			daemons:  []*db.Daemon{{ID: "daemon-a", UserID: "user-x"}},
			attached: []string{"daemon-a"},
		}))

	id, err := router.EnsureAwake(context.Background(), "user-x", &DaemonSelector{ID: "daemon-a"})
	require.NoError(t, err)
	assert.Equal(t, "daemon-a", id)
	assert.Empty(t, resumer.calls)
}

func TestRouterWithoutCredentialsOptionKeepsUsingUserJWT(t *testing.T) {
	auth.SetUserJWT("user-jwt-only", "jwt-1")
	t.Cleanup(func() { auth.SetUserJWT("user-jwt-only", "") })
	resumer := &fakeResumer{}
	router := NewNATSDaemonRouter(nil, WithDaemonResumer(resumer), WithDatabase(&fakeDaemonRecords{
		daemons: []*db.Daemon{{ID: "daemon-a", UserID: "user-jwt-only", LifecyclePhase: strPtr("suspended")}},
	}))

	id, err := router.EnsureAwake(context.Background(), "user-jwt-only", &DaemonSelector{ID: "daemon-a"})
	require.NoError(t, err)
	assert.Equal(t, "daemon-a", id)
	assert.Equal(t, []resumeCall{{token: "jwt-1", daemonID: "daemon-a"}}, resumer.calls)
}
