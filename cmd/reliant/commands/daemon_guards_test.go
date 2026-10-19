// Copyright (c) 2025 Reliant Labs
package commands

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/diskguard"
	"github.com/reliant-labs/reliant/internal/heavyjob"
	"github.com/reliant-labs/reliant/internal/runenv"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// guardsFor loads DaemonConfig the way `daemon start` does and returns the
// guard policy it hands the daemon runtime.
func guardsFor(t *testing.T, env map[string]string) daemonruntime.Guards {
	t.Helper()
	for _, k := range []string{"RELIANT_DISK_RECLAIM_AT", "RELIANT_DISK_REFUSE_AT", "RELIANT_HEAVY_COMMANDS"} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	runenv.ConfigureForTest(t, "")
	cmd, _, err := NewRootCmd().Find([]string{"daemon", "start"})
	require.NoError(t, err)
	require.NoError(t, cmd.ParseFlags(nil))
	dcfg, err := loadDaemonConfig(cmd)
	require.NoError(t, err)
	return daemonGuards(dcfg)
}

// The proto's heavy_commands default is the gate's own default: the two are
// one list written twice, and this is what keeps them one.
func TestDaemonGuards_DefaultHeavyCommandsAreTheGatesDefault(t *testing.T) {
	g := guardsFor(t, nil)
	assert.Equal(t, heavyjob.DefaultCommands, g.HeavyCommands)
	assert.Equal(t, diskguard.Config{}, g.Disk, "unset thresholds keep the guard's defaults")
}

func TestDaemonGuards_HeavyCommandsReplaceOrClearTheDefault(t *testing.T) {
	g := guardsFor(t, map[string]string{"RELIANT_HEAVY_COMMANDS": "cargo build, bazel test* ,,"})
	assert.Equal(t, []string{"cargo build", "bazel test*"}, g.HeavyCommands)

	g = guardsFor(t, map[string]string{"RELIANT_HEAVY_COMMANDS": ","})
	require.NotNil(t, g.HeavyCommands, "an empty list must not read as the default")
	assert.Empty(t, g.HeavyCommands)
}

func TestDaemonGuards_DiskThresholds(t *testing.T) {
	g := guardsFor(t, map[string]string{"RELIANT_DISK_RECLAIM_AT": "85", "RELIANT_DISK_REFUSE_AT": "off"})
	assert.InDelta(t, 0.85, g.Disk.ReclaimAt, 1e-9)
	assert.True(t, math.IsInf(g.Disk.RefuseAt, 1))

	// A threshold the guard cannot read keeps its default; the daemon still
	// starts.
	g = guardsFor(t, map[string]string{"RELIANT_DISK_RECLAIM_AT": "ninety", "RELIANT_DISK_REFUSE_AT": "96%"})
	assert.Zero(t, g.Disk.ReclaimAt)
	assert.InDelta(t, 0.96, g.Disk.RefuseAt, 1e-9)
}
