// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/pkg/config"
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
	assert.False(t, daemonHost(cfg, "").AllowPrivateFetch)

	t.Setenv("RELIANT_FETCH_ALLOW_PRIVATE_NETWORK", "true")
	cfg, err = loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.NoError(t, err)
	assert.True(t, cfg.GetFetchAllowPrivateNetwork())
	assert.True(t, daemonHost(cfg, "").AllowPrivateFetch)
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
	assert.Zero(t, daemonHost(cfg, "").Egress.UserPerMinute)
	assert.Zero(t, daemonHost(cfg, "").Egress.HostMaxConcurrent)

	for name, v := range envs {
		t.Setenv(name, v)
	}
	cfg, err = loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.NoError(t, err)
	got := daemonHost(cfg, "").Egress
	assert.Equal(t, 300, got.UserPerMinute)
	assert.Equal(t, 40, got.UserBurst)
	assert.Equal(t, 12, got.UserMaxConcurrent)
	assert.Equal(t, 9, got.HostMaxConcurrent)

	t.Setenv("RELIANT_DAEMON_EGRESS_USER_PER_MINUTE", "-1")
	_, err = loadDaemonConfig(daemonCommandWithArgs(t, "start"))
	require.Error(t, err, "a negative limit is refused at startup, like any other bad setting")
}

// `daemon start --label` declares the machine's durable labels: repeatable
// and comma-separated, it replaces RELIANT_DAEMON_LABELS, and a bad label is
// refused while the flags are parsed, before the daemon does anything.
func TestDaemonStartMachineLabels(t *testing.T) {
	t.Setenv("RELIANT_DAEMON_LABELS", "")
	require.NoError(t, os.Unsetenv("RELIANT_DAEMON_LABELS"))

	resolve := func(args ...string) (map[string]string, error) {
		cmd := daemonCommandWithArgs(t, "start", args...)
		cfg, err := loadDaemonConfig(cmd)
		if err != nil {
			return nil, err
		}
		return daemonMachineLabels(cmd, cfg, cmd.Flags().Lookup("label").Value.(*labelsFlag))
	}

	got, err := resolve()
	require.NoError(t, err)
	assert.Nil(t, got, "no flag and no config declares nothing")

	got, err = resolve("--label", "pool=pipeline", "--label", "gate=yes,size=2xl")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"pool": "pipeline", "gate": "yes", "size": "2xl"}, got)

	t.Setenv("RELIANT_DAEMON_LABELS", "pool=gate,team=ci")
	got, err = resolve()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"pool": "gate", "team": "ci"}, got)
	got, err = resolve("--label", "pool=pipeline")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"pool": "pipeline"}, got, "the flag replaces the env list")

	t.Setenv("RELIANT_DAEMON_LABELS", "")

	for _, bad := range []string{"pool", "Pool=pipeline", "pool=pipeline,gate"} {
		cmd, _, err := NewRootCmd().Find([]string{"daemon", "start"})
		require.NoError(t, err)
		err = cmd.ParseFlags([]string{"--label", bad})
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "--label", bad)
	}
}

// Only the label list in effect is checked: an invalid env/config list that
// --label replaces does not stop the daemon, and with no --label it does.
func TestDaemonStartLabelsCheckOnlyTheListInEffect(t *testing.T) {
	resolve := func(args ...string) (map[string]string, error) {
		cmd := daemonCommandWithArgs(t, "start", args...)
		cfg, err := loadDaemonConfig(cmd)
		if err != nil {
			return nil, err
		}
		return daemonMachineLabels(cmd, cfg, cmd.Flags().Lookup("label").Value.(*labelsFlag))
	}
	for _, bad := range []string{"Pool=x", "pool", "pool=pipeline,gate"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("RELIANT_DAEMON_LABELS", bad)

			got, err := resolve("--label", "pool=pipeline")
			require.NoError(t, err, "--label replaces the invalid env list")
			assert.Equal(t, map[string]string{"pool": "pipeline"}, got)

			_, err = resolve()
			require.Error(t, err, "the invalid env list is in effect without --label")
			assert.Contains(t, err.Error(), "RELIANT_DAEMON_LABELS")
		})
	}
}

// The same through `daemon start` itself: with an invalid env list and no
// --label it stops before doing anything, naming the setting.
func TestDaemonStartRefusesInvalidConfiguredLabels(t *testing.T) {
	t.Setenv("RELIANT_DAEMON_LABELS", "Pool=x")
	_, err := executeRoot(t, "daemon", "start", "--data-dir", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RELIANT_DAEMON_LABELS")
}

// unsetDaemonTypeEnv clears RELIANT_DAEMON_TYPE for the test (restored after).
func unsetDaemonTypeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RELIANT_DAEMON_TYPE", "")
	require.NoError(t, os.Unsetenv("RELIANT_DAEMON_TYPE"))
}

// daemonStartTypeFrom is the daemon type `daemon start --config file`
// declares, through the same loaders and precedence the command uses.
func daemonStartTypeFrom(t *testing.T, file string) (string, error) {
	t.Helper()
	cmd := daemonCommandWithArgs(t, "start", "--config", file)
	dcfg, err := loadDaemonConfig(cmd)
	if err != nil {
		return "", err
	}
	cliCfg, err := config.LoadCLIConfig(cmd)
	if err != nil {
		return "", err
	}
	return daemonHost(dcfg, cliCfg.GetMachine().GetDaemonType()).DaemonType, nil
}

// RELIANT_DAEMON_TYPE moved to CLIConfig's machine.daemon_type, but a
// `daemon start --config` file written for the old top-level daemon_type
// still declares the daemon type (deprecated, v1), with the old allowed
// values; machine.daemon_type wins when both are set.
func TestDaemonStartKeepsTheDeprecatedConfigFileDaemonType(t *testing.T) {
	unsetDaemonTypeEnv(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		return p
	}

	old := write("old.yaml", "daemonType: managed\n")
	got, err := daemonStartTypeFrom(t, old)
	require.NoError(t, err)
	assert.Equal(t, "managed", got, "the old top-level daemon_type no longer declares the type")

	got, err = daemonStartTypeFrom(t, write("snake.yaml", "daemon_type: managed\n"))
	require.NoError(t, err)
	assert.Equal(t, "managed", got)

	got, err = daemonStartTypeFrom(t, write("new.yaml", "machine:\n  daemonType: managed\n"))
	require.NoError(t, err)
	assert.Equal(t, "managed", got)

	t.Setenv("RELIANT_DAEMON_TYPE", "self_hosted")
	got, err = daemonStartTypeFrom(t, old)
	require.NoError(t, err)
	assert.Equal(t, "self_hosted", got, "machine.daemon_type (RELIANT_DAEMON_TYPE) wins over the deprecated field")

	unsetDaemonTypeEnv(t)
	_, err = daemonStartTypeFrom(t, write("bad.yaml", "daemonType: cloud\n"))
	require.Error(t, err, "the deprecated field keeps its allowed values")
}

// RELIANT_DAEMON_TYPE keeps working exactly as before for `auth login` and
// `auth logout`: managed is a cloud machine, anything else allowed is not,
// and a value outside the allowed set is refused.
func TestAuthCommandsReadRELIANTDaemonType(t *testing.T) {
	for _, path := range [][]string{{"auth", "login"}, {"auth", "logout"}} {
		cmd, _, err := NewRootCmd().Find(path)
		require.NoError(t, err)

		unsetDaemonTypeEnv(t)
		managed, err := isManagedMachine(cmd)
		require.NoError(t, err, path)
		assert.False(t, managed, path)

		t.Setenv("RELIANT_DAEMON_TYPE", "managed")
		managed, err = isManagedMachine(cmd)
		require.NoError(t, err, path)
		assert.True(t, managed, path)

		t.Setenv("RELIANT_DAEMON_TYPE", "self_hosted")
		managed, err = isManagedMachine(cmd)
		require.NoError(t, err, path)
		assert.False(t, managed, path)

		t.Setenv("RELIANT_DAEMON_TYPE", "cloud")
		_, err = isManagedMachine(cmd)
		require.Error(t, err, path)
	}
}
