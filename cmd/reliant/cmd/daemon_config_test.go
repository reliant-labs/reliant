// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The daemon's process-wide settings load from DaemonConfig, with their
// defaults when unset, and an unknown child priority stops the daemon (it
// used to mean "low" silently).
func TestDaemonProcessSettings(t *testing.T) {
	for _, name := range []string{"RELIANT_CHILD_PRIORITY", "RELIANT_WORKTREE_DIR", "RELIANT_CHROME_BROWSER_URL"} {
		t.Setenv(name, "") // restores the original afterwards
		require.NoError(t, os.Unsetenv(name))
	}
	cfg, err := loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.NoError(t, err)
	assert.Equal(t, "low", cfg.GetChildPriority())
	assert.Empty(t, cfg.GetWorktreeDir())
	assert.Empty(t, cfg.GetChromeBrowserUrl())

	t.Setenv("RELIANT_CHILD_PRIORITY", "normal")
	t.Setenv("RELIANT_WORKTREE_DIR", "/data/worktrees")
	t.Setenv("RELIANT_CHROME_BROWSER_URL", "http://host.k3d.internal:9222")
	cfg, err = loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.NoError(t, err)
	assert.Equal(t, "normal", cfg.GetChildPriority())
	assert.Equal(t, "/data/worktrees", cfg.GetWorktreeDir())
	assert.Equal(t, "http://host.k3d.internal:9222", cfg.GetChromeBrowserUrl())

	t.Setenv("RELIANT_CHILD_PRIORITY", "high")
	_, err = loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "child_priority")
}

// fetch's reach into the user's own network is an explicit opt-in: off unless
// RELIANT_FETCH_ALLOW_PRIVATE_NETWORK is set, and carried into the daemon's Host.
func TestDaemonFetchAllowPrivateNetworkIsAnOptIn(t *testing.T) {
	t.Setenv("RELIANT_FETCH_ALLOW_PRIVATE_NETWORK", "")
	require.NoError(t, os.Unsetenv("RELIANT_FETCH_ALLOW_PRIVATE_NETWORK"))
	cfg, err := loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.NoError(t, err)
	assert.False(t, cfg.GetFetchAllowPrivateNetwork())
	assert.False(t, daemonHost(cfg).AllowPrivateFetch)

	t.Setenv("RELIANT_FETCH_ALLOW_PRIVATE_NETWORK", "true")
	cfg, err = loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.NoError(t, err)
	assert.True(t, cfg.GetFetchAllowPrivateNetwork())
	assert.True(t, daemonHost(cfg).AllowPrivateFetch)
}

// The daemon's egress ceiling is configurable and unset means the daemon's
// own defaults (0 reaches daemonruntime as "default").
func TestDaemonEgressLimitsAreConfigurable(t *testing.T) {
	envs := map[string]string{
		"RELIANT_DAEMON_EGRESS_USER_PER_MINUTE":     "300",
		"RELIANT_DAEMON_EGRESS_USER_BURST":          "40",
		"RELIANT_DAEMON_EGRESS_USER_MAX_CONCURRENT": "12",
		"RELIANT_DAEMON_EGRESS_HOST_MAX_CONCURRENT": "9",
	}
	for name := range envs {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	cfg, err := loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.NoError(t, err)
	assert.Zero(t, daemonHost(cfg).Egress.UserPerMinute)
	assert.Zero(t, daemonHost(cfg).Egress.HostMaxConcurrent)

	for name, v := range envs {
		t.Setenv(name, v)
	}
	cfg, err = loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.NoError(t, err)
	got := daemonHost(cfg).Egress
	assert.Equal(t, 300, got.UserPerMinute)
	assert.Equal(t, 40, got.UserBurst)
	assert.Equal(t, 12, got.UserMaxConcurrent)
	assert.Equal(t, 9, got.HostMaxConcurrent)

	t.Setenv("RELIANT_DAEMON_EGRESS_USER_PER_MINUTE", "-1")
	_, err = loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.Error(t, err, "a negative limit is refused at startup, like any other bad setting")
}
