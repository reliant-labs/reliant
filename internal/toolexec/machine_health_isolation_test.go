package toolexec

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// TestMachineHealth_IsScopedToTheCallersOwnDaemons runs MachineHealth over the
// real registry (daemon rows plus attachment leases in Postgres), with two
// users whose daemons report different health. A health reason names process
// details of the machine, so a user must only ever read their own: by default
// and by naming their own daemon, and never by naming another user's daemon.
func TestMachineHealth_IsScopedToTheCallersOwnDaemons(t *testing.T) {
	database, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	managed := "managed"
	type machine struct {
		user, daemon string
		health       db.MachineHealth
	}
	alice := machine{
		user: "health-iso-alice", daemon: "health-iso-daemon-alice",
		health: db.MachineHealth{
			State: db.MachineHealthDegraded, Code: "process_table_full",
			Reason: "alice: 4096 of 4096 pids used by node",
			Since:  time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC),
		},
	}
	bob := machine{
		user: "health-iso-bob", daemon: "health-iso-daemon-bob",
		health: db.MachineHealth{
			State: db.MachineHealthDegraded, Code: "fork_failed",
			Reason: "bob: fork failed for /usr/bin/git",
			Since:  time.Date(2026, 11, 13, 9, 5, 0, 0, time.UTC),
		},
	}
	for _, m := range []machine{alice, bob} {
		require.NoError(t, database.UpsertDaemon(ctx, &db.Daemon{
			ID: m.daemon, UserID: m.user, DaemonType: &managed,
		}))
		require.NoError(t, database.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{
			DaemonID: m.daemon, UserID: m.user, Source: db.DaemonAttachmentSourceInbound,
		}))
		require.NoError(t, database.UpdateDaemonAttachmentHealth(ctx, m.daemon, m.health))
	}

	router := NewNATSDaemonRouter(nil, WithDatabase(database))
	sameHealth := func(t *testing.T, want, got db.MachineHealth) {
		t.Helper()
		require.Equal(t, want.State, got.State)
		require.Equal(t, want.Code, got.Code)
		require.Equal(t, want.Reason, got.Reason)
		require.True(t, want.Since.Equal(got.Since), "since = %v, want %v", got.Since, want.Since)
	}

	for _, tc := range []struct {
		self, other machine
	}{{alice, bob}, {bob, alice}} {
		t.Run(tc.self.user, func(t *testing.T) {
			for _, sel := range []*DaemonSelector{nil, {ID: tc.self.daemon}} {
				got, err := router.MachineHealth(ctx, tc.self.user, sel)
				require.NoError(t, err)
				sameHealth(t, tc.self.health, got)
			}
			// Naming the other user's machine discloses nothing: it is not a
			// machine of this user's.
			got, err := router.MachineHealth(ctx, tc.self.user, &DaemonSelector{ID: tc.other.daemon})
			require.NoError(t, err)
			require.Equal(t, db.MachineHealth{}, got, "selecting %s leaked %s's health", tc.other.daemon, tc.other.user)
		})
	}
}
