// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// newSuspendedRouter: the registry records one suspended managed daemon for
// userID; the resumer counts wakes.
func newSuspendedRouter(t *testing.T, userID string) (*NATSDaemonRouter, *fakeResumer) {
	t.Helper()
	resumer := &fakeResumer{}
	records := &fakeDaemonRecords{daemons: []*db.Daemon{{
		ID: "daemon-suspended", UserID: userID, LifecyclePhase: strPtr("suspended"),
	}}}
	return NewNATSDaemonRouter(nil, WithDatabase(records), WithDaemonResumer(resumer)), resumer
}

var pinned = &DaemonSelector{ID: "daemon-suspended"}

func TestToolRequestAgainstSuspendedPinnedDaemonIsPendingAndNeverResumes(t *testing.T) {
	router, resumer := newSuspendedRouter(t, "u")
	_, err := router.SendToolRequestSyncWithSelector(context.Background(), "u", &ToolExecutionRequest{RequestID: "r", ToolName: "bash"}, pinned)
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err), "got: %v", err)
	assert.Empty(t, resumer.calls, "a tool call must never resume a daemon")
}

func TestMCPCommandWithSelectorAgainstSuspendedDaemonIsPendingAndNeverResumes(t *testing.T) {
	router, resumer := newSuspendedRouter(t, "u")
	_, err := SendDaemonCommandForSelector(context.Background(), router, "u", pinned, "mcp.call", []byte(`{}`), 1000)
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err), "got: %v", err)
	assert.Empty(t, resumer.calls)
}

func TestDaemonCommandWithNilSelectorAgainstSuspendedDaemonIsPendingAndNeverResumes(t *testing.T) {
	router, resumer := newSuspendedRouter(t, "u")
	_, err := router.SendDaemonCommand(context.Background(), "u", "mcp.call", []byte(`{}`), 1000)
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err), "got: %v", err)
	_, err = router.SendToolRequestSync(context.Background(), "u", &ToolExecutionRequest{RequestID: "r", ToolName: "bash"})
	assert.True(t, IsDaemonPending(err), "got: %v", err)
	assert.Empty(t, resumer.calls)
}

func TestEnsureAwakeResumesTheSuspendedDaemonOnceWithTheUsersJWT(t *testing.T) {
	const userID = "user-ensure-awake"
	auth.SetUserJWT(userID, "jwt-attended")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })
	router, resumer := newSuspendedRouter(t, userID)

	id, err := router.EnsureAwake(context.Background(), userID, pinned)
	require.NoError(t, err)
	assert.Equal(t, "daemon-suspended", id)
	assert.Equal(t, []resumeCall{{token: "jwt-attended", daemonID: "daemon-suspended"}}, resumer.calls)
}

// A daemon that is attached, or still provisioning, is not EnsureAwake's to
// resume: the first is awake, the second is already on its way up and the
// control plane would refuse.
func TestEnsureAwakeResumesOnlySuspendedDaemons(t *testing.T) {
	const userID = "user-not-suspended"
	auth.SetUserJWT(userID, "jwt")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })
	resumer := &fakeResumer{}
	router := NewNATSDaemonRouter(nil, WithDaemonResumer(resumer), WithDatabase(&fakeDaemonRecords{
		daemons: []*db.Daemon{
			{ID: "daemon-live", UserID: userID, LifecyclePhase: strPtr("suspended")},
			{ID: "daemon-booting", UserID: userID, LifecyclePhase: strPtr("provisioning")},
		},
		attached: []string{"daemon-live"},
	}))

	id, err := router.EnsureAwake(context.Background(), userID, &DaemonSelector{ID: "daemon-live"})
	require.NoError(t, err)
	assert.Equal(t, "daemon-live", id)

	_, err = router.EnsureAwake(context.Background(), userID, &DaemonSelector{ID: "daemon-booting"})
	assert.True(t, IsDaemonPending(err), "got: %v", err)

	assert.Empty(t, resumer.calls)
}

// The registry's lifecycle is a mirror, and a managed machine suspended before
// the mirror existed (or whose events were lost) has no phase at all. The
// attended wake must not depend on it: an unattached managed daemon is asked
// about, and the control plane — the authority — decides.
func TestEnsureAwakeAsksTheControlPlaneAboutAnUnattachedManagedDaemonWithNoLifecycle(t *testing.T) {
	const userID = "user-no-mirror"
	auth.SetUserJWT(userID, "jwt")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })

	for _, tc := range []struct {
		name      string
		resumeErr error
	}{
		{name: "control plane resumes it"},
		{name: "control plane says it is not suspended", resumeErr: connect.NewError(connect.CodeFailedPrecondition, nil)},
		{name: "control plane does not know it", resumeErr: connect.NewError(connect.CodeNotFound, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resumer := &fakeResumer{err: tc.resumeErr}
			router := NewNATSDaemonRouter(nil, WithDaemonResumer(resumer), WithDatabase(&fakeDaemonRecords{
				daemons: []*db.Daemon{{ID: "cloud", UserID: userID, DaemonType: strPtr("managed")}},
			}))

			id, err := router.EnsureAwake(context.Background(), userID, nil)
			require.NoError(t, err, "a speculative resume never makes the wake worse than routing as-is")
			assert.Equal(t, "cloud", id)
			assert.Equal(t, []resumeCall{{token: "jwt", daemonID: "cloud"}}, resumer.calls)
		})
	}
}

// A self-hosted daemon has nothing the control plane can resume.
func TestEnsureAwakeNeverAsksToResumeASelfHostedDaemon(t *testing.T) {
	const userID = "user-laptop"
	auth.SetUserJWT(userID, "jwt")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })
	resumer := &fakeResumer{}
	router := NewNATSDaemonRouter(nil, WithDaemonResumer(resumer), WithDatabase(&fakeDaemonRecords{
		daemons: []*db.Daemon{{ID: "laptop", UserID: userID, DaemonType: strPtr("self_hosted")}},
	}))

	id, err := router.EnsureAwake(context.Background(), userID, nil)
	require.NoError(t, err)
	assert.Equal(t, "laptop", id)
	assert.Empty(t, resumer.calls)
}

// A refusal other than "no longer suspended" is a real failure and is
// reported, not swallowed.
func TestEnsureAwakeReportsAResumeRefusal(t *testing.T) {
	const userID = "user-resume-refused"
	auth.SetUserJWT(userID, "jwt")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })
	router, resumer := newSuspendedRouter(t, userID)
	resumer.err = connect.NewError(connect.CodeResourceExhausted, nil)

	_, err := router.EnsureAwake(context.Background(), userID, pinned)
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
}

// Wake reports whether it resumed anything. A user's file and worktree
// requests say "waking" only when a machine actually was asleep, so a machine
// that is already up, or still on its way up, keeps its own copy.
func TestWakeReportsWhetherItResumed(t *testing.T) {
	const userID = "user-wake-report"
	auth.SetUserJWT(userID, "jwt-attended")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })

	router, resumer := newSuspendedRouter(t, userID)
	res, err := router.Wake(context.Background(), userID, pinned)
	require.NoError(t, err)
	assert.Equal(t, WakeResult{DaemonID: "daemon-suspended", Resumed: true}, res)
	assert.Len(t, resumer.calls, 1)

	// Attached: awake, nothing to ask.
	live := NewNATSDaemonRouter(nil, WithDaemonResumer(&fakeResumer{}), WithDatabase(&fakeDaemonRecords{
		daemons:  []*db.Daemon{{ID: "daemon-live", UserID: userID, LifecyclePhase: strPtr("suspended")}},
		attached: []string{"daemon-live"},
	}))
	res, err = live.Wake(context.Background(), userID, &DaemonSelector{ID: "daemon-live"})
	require.NoError(t, err)
	assert.Equal(t, WakeResult{DaemonID: "daemon-live"}, res)

	// The mirror says suspended but the control plane no longer holds it so
	// (an earlier request woke it): treated as awake, and not a fresh wake.
	refused := NewNATSDaemonRouter(nil,
		WithDaemonResumer(&fakeResumer{err: connect.NewError(connect.CodeFailedPrecondition, nil)}),
		WithDatabase(&fakeDaemonRecords{daemons: []*db.Daemon{
			{ID: "daemon-waking", UserID: userID, LifecyclePhase: strPtr("suspended")},
		}}))
	res, err = refused.Wake(context.Background(), userID, &DaemonSelector{ID: "daemon-waking"})
	require.NoError(t, err)
	assert.Equal(t, WakeResult{DaemonID: "daemon-waking"}, res)
}

// The structural guard: tool-time code holds a DaemonRouter, which must have
// no way to wake. If someone adds a wake method to the interface, this fails.
func TestToolTimeInterfacesHaveNoWakeMethod(t *testing.T) {
	for _, iface := range []reflect.Type{
		reflect.TypeOf((*DaemonRouter)(nil)).Elem(),
		reflect.TypeOf((*selectorCommandSender)(nil)).Elem(),
	} {
		for i := 0; i < iface.NumMethod(); i++ {
			name := iface.Method(i).Name
			assert.NotContains(t, name, "Wake", "%s.%s", iface.Name(), name)
			assert.NotContains(t, name, "Resume", "%s.%s", iface.Name(), name)
			assert.NotEqual(t, "EnsureAwake", name, "%s", iface.Name())
		}
	}
	assert.NotNil(t, reflect.TypeOf((*DaemonWaker)(nil)).Elem().MethodByName)
}
