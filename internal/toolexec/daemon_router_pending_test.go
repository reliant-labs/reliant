// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// (a) A daemon record exists but is not routable yet (provisioning, as the
// registry's mirrored lifecycle says) must resolve to ErrDaemonPending, not a
// flat "no daemon" error.
func TestResolveDaemonID_DaemonExistsButNotConnected_ReturnsPendingSignal(t *testing.T) {
	router := recordsRouter(&fakeDaemonRecords{daemons: []*db.Daemon{{
		ID: "daemon-provisioning", UserID: "user-1", LifecyclePhase: strPtr("provisioning"),
	}}})

	_, err := router.resolveDaemonID(context.Background(), "user-1", nil)
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err), "expected ErrDaemonPending, got: %v", err)
	assert.Contains(t, err.Error(), "no daemon connected",
		"message must carry the marker the frontend's isDaemonConnectingError keys on")
}

// (b) When no daemon record exists at all, the router must still return a
// hard, non-pending error — the genuine-error path must not be softened into
// an infinite wait.
func TestResolveDaemonID_NoDaemonAtAll_ReturnsHardError(t *testing.T) {
	router := recordsRouter(&fakeDaemonRecords{})

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
		daemons  []*db.Daemon
		selector *DaemonSelector
	}{
		{
			name: "no daemon at all",
		},
		{
			name: "daemon record exists but is not routable",
			daemons: []*db.Daemon{{
				ID: "daemon-provisioning", UserID: userID, LifecyclePhase: strPtr("provisioning"),
			}},
		},
		{
			name: "daemon record is suspended",
			daemons: []*db.Daemon{{
				ID: "daemon-suspended", UserID: userID, LifecyclePhase: strPtr("suspended"),
			}},
		},
		{
			name:     "no daemon matching an explicit selector",
			selector: &DaemonSelector{Type: "cloud", Name: "box", ID: "d-1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := recordsRouter(&fakeDaemonRecords{daemons: tc.daemons})

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
	router := recordsRouter(&fakeDaemonRecords{daemons: []*db.Daemon{{
		ID: "daemon-provisioning", UserID: "user-1",
		DaemonType: strPtr("managed"), LifecyclePhase: strPtr("provisioning"),
	}}})

	_, err := router.resolveDaemonID(context.Background(), "user-1", &DaemonSelector{Type: "cloud"})
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err))
	assert.Contains(t, err.Error(), "no daemon connected",
		"isDaemonConnectingError keys on this marker")
}

// A daemon that is attached must return the id with no error, pending or
// otherwise.
func TestResolveDaemonID_DaemonResolves_ReturnsID(t *testing.T) {
	router := recordsRouter(&fakeDaemonRecords{
		daemons:  []*db.Daemon{{ID: "daemon-active", UserID: "user-1"}},
		attached: []string{"daemon-active"},
	})

	id, err := router.resolveDaemonID(context.Background(), "user-1", nil)
	require.NoError(t, err)
	assert.Equal(t, "daemon-active", id)
}
