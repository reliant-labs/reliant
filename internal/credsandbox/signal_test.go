// Copyright (c) 2026 Reliant Labs

package credsandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The control-plane pipeline's credentialed steps fail closed on anything but
// exactly this name with a version of at least 2: changing either stops them.
func TestSignal_NameAndVersionAreTheContract(t *testing.T) {
	assert.Equal(t, "RELIANT_CREDSANDBOX_VERSION", SignalEnv)
	assert.Equal(t, 2, SignalVersion)
	assert.Equal(t, ".reliant/cred-scratch", filepath.ToSlash(CredScratchRel))
}

func TestWithSignal_SetsItOnlyWhenTheSandboxIsInForce(t *testing.T) {
	env := []string{"A=1", SignalEnv + "=7", "B=2"}

	assert.Equal(t, []string{"A=1", "B=2", SignalEnv + "=2"}, withSignal(env, true))
	assert.Equal(t, []string{"A=1", "B=2"}, withSignal(env, false),
		"an inherited value says nothing about this daemon's sandbox")
}

func TestDefaultPlan_HidesAndSealsTheCredentialedScratch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := DefaultPlan()
	scratch := filepath.Join(home, ".reliant", "cred-scratch")
	assert.Contains(t, p.Hide, scratch)
	assert.Contains(t, p.Seal, scratch)
}

func TestEnsureCredScratch_CreatesItPrivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ensureCredScratch()
	fi, err := os.Lstat(filepath.Join(home, ".reliant", "cred-scratch"))
	require.NoError(t, err)
	assert.True(t, fi.IsDir())
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	}
}

func TestEnsureCredScratch_TightensAnExistingOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	scratch := filepath.Join(home, ".reliant", "cred-scratch")
	require.NoError(t, os.MkdirAll(scratch, 0o777))
	require.NoError(t, os.Chmod(scratch, 0o777))
	ensureCredScratch()
	fi, err := os.Stat(scratch)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
}

// A link planted where the scratch goes (by a credentials-none command of an
// older daemon) is moved aside, never followed.
func TestEnsureCredScratch_MovesAPlantedLinkAside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	elsewhere := t.TempDir()
	scratch := filepath.Join(home, ".reliant", "cred-scratch")
	require.NoError(t, os.MkdirAll(filepath.Dir(scratch), 0o755))
	require.NoError(t, os.Symlink(elsewhere, scratch))

	ensureCredScratch()
	fi, err := os.Lstat(scratch)
	require.NoError(t, err)
	assert.True(t, fi.IsDir(), "a real directory now")
	assert.Zero(t, fi.Mode()&os.ModeSymlink)
	aside, _ := filepath.Glob(scratch + ".untrusted-*")
	assert.Len(t, aside, 1, "the planted link is kept for inspection")
	entries, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was written through the link")
}
