// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// MachineState is how a run waiting for its machine tells "still coming up"
// from "not coming": the record routing would pick, read from the registry.
func TestMachineState(t *testing.T) {
	managed := "managed"
	failedMachine := &db.Daemon{ID: "m-failed", UserID: "u", DaemonType: &managed,
		LifecyclePhase: strPtr("failed"), LastStatusMessage: "tools-daemon exited with code 1"}
	startingMachine := &db.Daemon{ID: "m-starting", UserID: "u", DaemonType: &managed,
		LifecyclePhase: strPtr("provisioning")}

	cases := []struct {
		name     string
		records  *fakeDaemonRecords
		selector *DaemonSelector
		want     MachineState
	}{
		{
			name:    "a machine that failed to start, and is not attached, is failed",
			records: &fakeDaemonRecords{daemons: []*db.Daemon{failedMachine}},
			want: MachineState{DaemonID: "m-failed", Exists: true, Failed: true,
				StatusMessage: "tools-daemon exited with code 1"},
		},
		{
			name:    "an attached machine is never failed, whatever its mirrored phase says",
			records: &fakeDaemonRecords{daemons: []*db.Daemon{failedMachine}, attached: []string{"m-failed"}},
			want: MachineState{DaemonID: "m-failed", Exists: true,
				StatusMessage: "tools-daemon exited with code 1"},
		},
		{
			name:    "a machine still provisioning is coming up",
			records: &fakeDaemonRecords{daemons: []*db.Daemon{startingMachine}},
			want:    MachineState{DaemonID: "m-starting", Exists: true},
		},
		{
			name:    "routing picks the starting machine over a failed one, so the run waits for it",
			records: &fakeDaemonRecords{daemons: []*db.Daemon{failedMachine, startingMachine}},
			want:    MachineState{DaemonID: "m-starting", Exists: true},
		},
		{
			name:     "a pinned machine that is gone does not exist",
			records:  &fakeDaemonRecords{daemons: []*db.Daemon{startingMachine}},
			selector: &DaemonSelector{ID: "m-removed"},
			want:     MachineState{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := NewNATSDaemonRouter(nil, WithDatabase(tc.records))
			got, err := router.MachineState(context.Background(), "u", tc.selector)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// With no registry, "no record" means nothing — it must not read as removed.
func TestMachineState_WithoutARegistryIsUnknown(t *testing.T) {
	router := NewNATSDaemonRouter(nil)
	_, err := router.MachineState(context.Background(), "u", nil)
	require.Error(t, err)
}
