// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/diskguard"
	"github.com/reliant-labs/reliant/internal/heavyjob"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/proctree"
	"github.com/reliant-labs/reliant/internal/runenv"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// guardsFor loads DaemonConfig the way `daemon start` does and returns the
// guard policy it hands the daemon runtime.
func guardsFor(t *testing.T, env map[string]string) daemonruntime.Guards {
	t.Helper()
	for _, k := range []string{"RELIANT_DISK_RECLAIM_AT", "RELIANT_DISK_REFUSE_AT", "RELIANT_HEAVY_COMMANDS", "RELIANT_HEAVY_SHARE", "RELIANT_COMMAND_THREADS", "RELIANT_BACKGROUND_IDLE_REAP"} {
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
	assert.Zero(t, g.HeavyShare, "an unset share is derived from the slot pool")
	assert.Zero(t, g.CommandThreads, "an unset thread limit is derived from the machine")
}

func TestDaemonGuards_HeavyShare(t *testing.T) {
	g := guardsFor(t, map[string]string{"RELIANT_HEAVY_SHARE": "6"})
	assert.Equal(t, 6, g.HeavyShare)
}

// The env var a command at its thread limit is told to raise is the one
// command_threads reads.
func TestDaemonGuards_CommandThreads(t *testing.T) {
	g := guardsFor(t, map[string]string{"RELIANT_COMMAND_THREADS": "4096"})
	assert.Equal(t, 4096, g.CommandThreads)
	assert.Contains(t, proctree.CommandThreadsSetting, "command_threads")
	assert.Contains(t, proctree.CommandThreadsSetting, "RELIANT_COMMAND_THREADS")
}

// background_idle_reap, loaded the way `daemon start` loads it, reaches the
// runtime's idle reaper: unset is the 4h default, a duration is that window,
// and an explicit 0 turns idle reaping off rather than reading as "unset".
func TestDaemonGuards_BackgroundIdleReap(t *testing.T) {
	assert.Equal(t, shell.DefaultIdleReap, guardsFor(t, nil).IdleReapWindow())
	assert.Equal(t, 4*time.Hour, shell.DefaultIdleReap, "the owner's window (2026-11-02)")
	assert.Equal(t, 90*time.Minute, guardsFor(t, map[string]string{"RELIANT_BACKGROUND_IDLE_REAP": "90m"}).IdleReapWindow())
	assert.Zero(t, guardsFor(t, map[string]string{"RELIANT_BACKGROUND_IDLE_REAP": "0"}).IdleReapWindow(), "0 is off")
	// The runtime's own zero value (an entry point that sets no Guards) is
	// the default, not off.
	assert.Equal(t, shell.DefaultIdleReap, daemonruntime.Guards{}.IdleReapWindow())
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
