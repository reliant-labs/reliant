// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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

// Without a data directory, only what this process made is trusted: it makes
// the scratch where there is none and keeps it.
func TestEnsureCredScratch_WithoutARecordTrustsOnlyWhatThisProcessMade(t *testing.T) {
	dir, _ := scratchAt(t)
	require.NoError(t, ensureCredScratchAt(dir, ""))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mine"), nil, 0o600))
	require.NoError(t, ensureCredScratchAt(dir, ""))
	assert.FileExists(t, filepath.Join(dir, "mine"))
}

// #2823: a process with no data directory (a test binary an agent runs) found
// the daemon's recorded scratch, could not see the record, and moved it aside;
// the daemon then moved the replacement aside in turn, hundreds of times a
// day. Now it leaves a scratch it did not make alone, contents and all, and
// trusts it no more than before: an error, so its command gets no signal.
func TestEnsureCredScratch_WithoutARecordLeavesAScratchItDidNotMake(t *testing.T) {
	dir, record := scratchAt(t)
	require.NoError(t, ensureCredScratchAt(dir, record)) // the daemon's
	require.NoError(t, os.WriteFile(filepath.Join(dir, "step"), []byte("daemon's"), 0o600))
	scratchState.made = "" // another process: no record, nothing made

	require.ErrorIs(t, ensureCredScratchAt(dir, ""), errScratchNotMade)

	got, err := os.ReadFile(filepath.Join(dir, "step"))
	require.NoError(t, err)
	assert.Equal(t, "daemon's", string(got))
	aside, _ := filepath.Glob(dir + movedAsideSuffix + "*")
	assert.Empty(t, aside, "nothing was moved aside")
	require.NoError(t, ensureCredScratchAt(dir, record), "the daemon still trusts its own")
}

// What is moved aside is kept for inspection for a day, then pruned; nothing
// else beside the scratch is touched, and a moved-aside link is unlinked, not
// followed.
func TestPruneMovedAside_RemovesOnlyExpiredMovedAsideEntries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".reliant", "cred-scratch")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	now := time.Now()
	stamp := func(at time.Time) string { return dir + movedAsideSuffix + strconv.FormatInt(at.UnixNano(), 10) }

	old := stamp(now.Add(-25 * time.Hour))
	require.NoError(t, os.MkdirAll(filepath.Join(old, "pr-fix"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(old, "pr-fix", "run.sh"), nil, 0o600))
	target := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(target, "keep"), nil, 0o600))
	oldLink := stamp(now.Add(-48 * time.Hour))
	require.NoError(t, os.Symlink(target, oldLink))
	recent := stamp(now.Add(-time.Hour))
	require.NoError(t, os.Mkdir(recent, 0o700))
	unrelated := []string{dir + movedAsideSuffix + "notatime", filepath.Join(filepath.Dir(dir), "other.untrusted-1")}
	for _, p := range unrelated {
		require.NoError(t, os.Mkdir(p, 0o700))
	}

	removed, err := pruneMovedAsideAt(dir, now, movedAsideRetention)
	require.NoError(t, err)

	assert.Equal(t, 2, removed)
	assert.NoDirExists(t, old)
	assert.NoFileExists(t, oldLink)
	assert.FileExists(t, filepath.Join(target, "keep"), "the link was not followed")
	assert.DirExists(t, recent, "kept for inspection for a day")
	assert.DirExists(t, dir, "the scratch itself")
	for _, p := range unrelated {
		assert.DirExists(t, p)
	}
}

func TestPruneMovedAside_NoParentIsNothingToDo(t *testing.T) {
	removed, err := pruneMovedAsideAt(filepath.Join(t.TempDir(), "absent", "cred-scratch"), time.Now(), movedAsideRetention)
	require.NoError(t, err)
	assert.Zero(t, removed)
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
