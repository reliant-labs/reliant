// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/runenv"
)

// storageReaperFor loads the root config the way `daemon start` does and
// returns the schedule it hands the daemon runtime.
func storageReaperFor(t *testing.T, env map[string]string, args ...string) diskwatch.ReaperConfig {
	t.Helper()
	for _, k := range []string{"RELIANT_STORAGE_REAPER", "RELIANT_STORAGE_REAPER_INTERVAL", "RELIANT_STORAGE_REAPER_PRESSURE_INTERVAL", "FORGE_STORAGE_POLICY", "RELIANT_DAEMON_TYPE"} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	runenv.ConfigureForTest(t, "")
	cmd, _, err := NewRootCmd().Find([]string{"daemon", "start"})
	require.NoError(t, err)
	require.NoError(t, cmd.ParseFlags(args))
	cfg, err := loadRootConfig(cmd)
	require.NoError(t, err)
	dcfg, err := loadDaemonConfig(cmd)
	require.NoError(t, err)
	return storageReaperConfig(cfg, daemonHost(dcfg).Managed())
}

func TestStorageReaperConfig_DefaultsFollowTheDaemonType(t *testing.T) {
	managed := storageReaperFor(t, map[string]string{"RELIANT_DAEMON_TYPE": "managed"})
	assert.True(t, managed.Enabled, "a managed daemon (a workspace pod) runs the reaper by default")
	assert.Equal(t, time.Hour, managed.Interval)
	assert.Equal(t, 10*time.Minute, managed.PressureInterval)
	assert.Empty(t, managed.PolicyPath, "unset: forge's own default location")

	local := storageReaperFor(t, nil)
	assert.False(t, local.Enabled, "a person's own machine is left alone unless they opt in")
}

func TestStorageReaperConfig_EnvOverrides(t *testing.T) {
	cfg := storageReaperFor(t, map[string]string{
		"RELIANT_STORAGE_REAPER":                   "on",
		"RELIANT_STORAGE_REAPER_INTERVAL":          "30m",
		"RELIANT_STORAGE_REAPER_PRESSURE_INTERVAL": "2m",
		"FORGE_STORAGE_POLICY":                     "/var/cache/workspace/forge-storage.json",
	})
	assert.True(t, cfg.Enabled)
	assert.Equal(t, 30*time.Minute, cfg.Interval)
	assert.Equal(t, 2*time.Minute, cfg.PressureInterval)
	assert.Equal(t, "/var/cache/workspace/forge-storage.json", cfg.PolicyPath)
}

func TestStorageReaperConfig_OptOutWinsOnAManagedDaemon(t *testing.T) {
	cfg := storageReaperFor(t, map[string]string{"RELIANT_DAEMON_TYPE": "managed", "RELIANT_STORAGE_REAPER": "off"})
	assert.False(t, cfg.Enabled)
}
