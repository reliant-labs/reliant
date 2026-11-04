// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// A pin records a machine; it must not wait for that machine to come up. A
// starting or suspended machine answers with its record id, where
// ResolveDaemonID answers ErrDaemonPending.
func TestPlacementDaemonID_AcceptsAMachineThatIsStartingOrAsleep(t *testing.T) {
	for _, phase := range []string{"provisioning", "cloning", "suspended", "suspending"} {
		t.Run(phase, func(t *testing.T) {
			router := recordsRouter(&fakeDaemonRecords{daemons: []*db.Daemon{{
				ID: "vm", UserID: "u", LifecyclePhase: strPtr(phase),
			}}})

			_, resolveErr := router.ResolveDaemonID(context.Background(), "u")
			require.True(t, IsDaemonPending(resolveErr), "precondition: routing refuses it")

			for _, want := range []string{"", "vm"} {
				id, err := router.PlacementDaemonID(context.Background(), "u", want)
				require.NoError(t, err)
				assert.Equal(t, "vm", id)
			}
		})
	}
}

// "The user has no such machine" stays an error: a pin must never name a
// machine the user does not own.
func TestPlacementDaemonID_RefusesAMachineTheUserDoesNotHave(t *testing.T) {
	router := recordsRouter(&fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "mine", UserID: "u"},
		{ID: "theirs", UserID: "someone-else"},
	}})

	_, err := router.PlacementDaemonID(context.Background(), "u", "theirs")
	require.Error(t, err)
	assert.False(t, IsDaemonPending(err))

	_, err = recordsRouter(&fakeDaemonRecords{}).PlacementDaemonID(context.Background(), "u", "")
	assert.True(t, IsNoDaemon(err), "a user with no machine at all")
}

// A machine that is up resolves exactly as routing does.
func TestPlacementDaemonID_AttachedMachineIsResolvedNormally(t *testing.T) {
	router := recordsRouter(&fakeDaemonRecords{
		daemons:  []*db.Daemon{{ID: "up", UserID: "u"}, {ID: "asleep", UserID: "u", LifecyclePhase: strPtr("suspended")}},
		attached: []string{"up"},
	})
	id, err := router.PlacementDaemonID(context.Background(), "u", "")
	require.NoError(t, err)
	assert.Equal(t, "up", id)
}
