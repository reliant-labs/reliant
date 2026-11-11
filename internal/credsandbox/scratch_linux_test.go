// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scratchAt is a credentialed scratch path and its record in a data directory
// of the test's own, with this process having made nothing yet.
func scratchAt(t *testing.T) (dir, record string) {
	t.Helper()
	saved := scratchState.made
	scratchState.made = ""
	t.Cleanup(func() { scratchState.made = saved })
	home, data := t.TempDir(), t.TempDir()
	return filepath.Join(home, ".reliant", "cred-scratch"), filepath.Join(data, credScratchRecordName)
}

func TestEnsureCredScratch_MakesItPrivateAndRecordsIt(t *testing.T) {
	dir, record := scratchAt(t)
	require.NoError(t, ensureCredScratchAt(dir, record))
	fi, err := os.Lstat(dir)
	require.NoError(t, err)
	assert.True(t, fi.IsDir())
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	raw, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Equal(t, identity(fi)+"\n", string(raw))
}

// R2.B3: a real directory of the daemon's user there is not trusted for that:
// one an older daemon's credentials-none command made, with its contents, is
// moved aside, and the credentialed commands get a fresh, empty one.
func TestEnsureCredScratch_ReplacesADirectoryNoVersion2DaemonMade(t *testing.T) {
	dir, record := scratchAt(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pr-fix"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pr-fix", "run.sh"), []byte("planted"), 0o700))

	require.NoError(t, ensureCredScratchAt(dir, record))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the planted contents are not in the scratch credentialed commands use")
	aside, _ := filepath.Glob(dir + ".untrusted-*")
	require.Len(t, aside, 1, "kept for inspection")
	assert.FileExists(t, filepath.Join(aside[0], "pr-fix", "run.sh"))
}

// The one it made and recorded is kept, contents and all: a credentialed
// step's scratch survives the next command and a daemon restart.
func TestEnsureCredScratch_KeepsTheOneItRecorded(t *testing.T) {
	dir, record := scratchAt(t)
	require.NoError(t, ensureCredScratchAt(dir, record))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "step"), []byte("mine"), 0o600))
	scratchState.made = "" // a restarted daemon: only the record remains
	require.NoError(t, os.Chmod(dir, 0o755))

	require.NoError(t, ensureCredScratchAt(dir, record))
	got, err := os.ReadFile(filepath.Join(dir, "step"))
	require.NoError(t, err)
	assert.Equal(t, "mine", string(got))
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), "and tightened")
}

// A directory put where the recorded one was is not it.
func TestEnsureCredScratch_ReplacesADirectorySwappedInForTheRecordedOne(t *testing.T) {
	dir, record := scratchAt(t)
	require.NoError(t, ensureCredScratchAt(dir, record))
	require.NoError(t, os.Rename(dir, dir+".gone"))
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "planted"), nil, 0o600))

	require.NoError(t, ensureCredScratchAt(dir, record))
	assert.NoFileExists(t, filepath.Join(dir, "planted"))
}

// Without a data directory, only what this process made is trusted.
func TestEnsureCredScratch_WithoutARecordTrustsOnlyWhatThisProcessMade(t *testing.T) {
	dir, _ := scratchAt(t)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "planted"), nil, 0o600))
	require.NoError(t, ensureCredScratchAt(dir, ""))
	assert.NoFileExists(t, filepath.Join(dir, "planted"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mine"), nil, 0o600))
	require.NoError(t, ensureCredScratchAt(dir, ""))
	assert.FileExists(t, filepath.Join(dir, "mine"))
}

// R2.B4: the scratch is made only for a credentialed command where the sandbox
// is in force, and a scratch that cannot be made means no signal: a step that
// needs version 2 fails closed rather than trust a scratch that is not there.
func TestWrap_TheScratchIsMadeOnlyWhereTheSandboxIsInForce(t *testing.T) {
	testkit.Slow(t, "probes the sandbox with real user, mount and PID namespaces")
	requireSandbox(t)
	envOf := func(t *testing.T) []string {
		t.Helper()
		cmd := exec.Command("true")
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		require.Empty(t, Wrap(cmd, "inherit", Writable{}))
		return cmd.Env
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	disabled.Store(true)
	env := envOf(t)
	disabled.Store(false)
	assert.NoDirExists(t, filepath.Join(home, CredScratchRel), "not where the sandbox is off")
	assert.NotContains(t, env, SignalEnv+"=2")

	env = envOf(t)
	assert.DirExists(t, filepath.Join(home, CredScratchRel))
	assert.Contains(t, env, SignalEnv+"=2")

	blockedHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(blockedHome, ".reliant"), nil, 0o600))
	t.Setenv("HOME", blockedHome)
	assert.NotContains(t, envOf(t), SignalEnv+"=2", "a scratch it cannot make: no signal")
}

// A link planted where the scratch goes is moved aside, never followed.
func TestEnsureCredScratch_MovesAPlantedLinkAside(t *testing.T) {
	dir, record := scratchAt(t)
	elsewhere := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o755))
	require.NoError(t, os.Symlink(elsewhere, dir))

	require.NoError(t, ensureCredScratchAt(dir, record))
	fi, err := os.Lstat(dir)
	require.NoError(t, err)
	assert.True(t, fi.IsDir(), "a real directory now")
	entries, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was written through the link")
}
