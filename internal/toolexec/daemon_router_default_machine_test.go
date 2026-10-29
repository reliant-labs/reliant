// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// Prod 2026-10-29: the owner added a CI machine, and work that named no
// machine followed whichever machine was connected and listed first. A
// machine the user made their default (daemons.is_default) is what default
// resolution means from then on — in whatever state it is: a sleeping default
// is woken, never passed over for the connected CI machine.
func defaultMachineRecords(userID string) *fakeDaemonRecords {
	return &fakeDaemonRecords{
		daemons: []*db.Daemon{
			{ID: "cicd", UserID: userID},
			{ID: "default", UserID: userID, LifecyclePhase: strPtr("suspended"), IsDefault: true},
			// Another user's default never applies here.
			{ID: "theirs", UserID: "someone-else", IsDefault: true},
		},
		attached: []string{"cicd"},
	}
}

func TestDefaultMachine_DefaultResolutionPicksItOverAConnectedMachine(t *testing.T) {
	router := recordsRouter(defaultMachineRecords("u"))
	ctx := context.Background()

	_, err := router.ResolveDaemonID(ctx, "u")
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err), "the asleep default is waited for, not replaced by cicd: %v", err)

	id, err := router.PlacementDaemonID(ctx, "u", "")
	require.NoError(t, err)
	assert.Equal(t, "default", id, "a new chat is pinned to the default")

	state, err := router.MachineState(ctx, "u", nil)
	require.NoError(t, err)
	assert.Equal(t, "default", state.DaemonID)
	assert.True(t, state.Suspended)
}

func TestDefaultMachine_WakeResumesTheDefault(t *testing.T) {
	const userID = "user-default-wake"
	auth.SetUserJWT(userID, "jwt-attended")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })

	resumer := &fakeResumer{}
	router := NewNATSDaemonRouter(nil, WithDatabase(defaultMachineRecords(userID)), WithDaemonResumer(resumer))
	res, err := router.Wake(context.Background(), userID, nil)
	require.NoError(t, err)
	assert.Equal(t, "default", res.DaemonID)
	require.Len(t, resumer.calls, 1)
	assert.Equal(t, "default", resumer.calls[0].daemonID)
}

func TestDefaultMachine_ExplicitSelectorIsUnaffected(t *testing.T) {
	router := recordsRouter(defaultMachineRecords("u"))
	id, err := router.ResolveDaemonIDForSelector(context.Background(), "u", &DaemonSelector{ID: "cicd"})
	require.NoError(t, err)
	assert.Equal(t, "cicd", id, "an automation's or a pinned chat's machine is its own")
}

func TestDefaultMachine_NoDefaultKeepsTheMachineStateRule(t *testing.T) {
	records := defaultMachineRecords("u")
	records.daemons[1].IsDefault = false // cleared, or the default machine was deleted
	router := recordsRouter(records)
	id, err := router.ResolveDaemonID(context.Background(), "u")
	require.NoError(t, err)
	assert.Equal(t, "cicd", id)

	records.daemons = records.daemons[:1] // the default row is gone entirely
	id, err = router.PlacementDaemonID(context.Background(), "u", "")
	require.NoError(t, err)
	assert.Equal(t, "cicd", id)
}
