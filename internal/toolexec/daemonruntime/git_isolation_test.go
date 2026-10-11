// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// processStart* are captured during package initialisation, which runs before
// TestMain, so they hold what the invoking user's environment looked like.
var (
	processStartHome         = os.Getenv("HOME")
	processStartGitConfigEnv = os.Getenv("GIT_CONFIG_GLOBAL")
)

// Running this package must never modify the invoking user's git config, even
// when the invoker is itself a managed workspace. A managed Start installs the
// github.com credential helper pointing at os.Executable() — here a temporary
// test binary — into the global git config; if that were the real
// ~/.gitconfig, every later git fetch/push on the machine would fail with
// "…daemonruntime.test: not found".
//
// TestMain (main_test.go) isolates HOME and the git config. This test pins the
// property from the outside: it forces the managed path and checks that the
// config the process started with was untouched, while the wiring did run.
func TestManagedStartNeverTouchesTheInvokingUsersGitConfig(t *testing.T) {
	require.NotEmpty(t, processStartHome, "need the HOME the process started with")
	require.NotEqual(t, processStartHome, os.Getenv("HOME"), "TestMain must replace HOME for the whole package")

	// Everywhere git could resolve the invoking user's global config.
	invoking := []string{filepath.Join(processStartHome, ".gitconfig"), filepath.Join(processStartHome, ".config", "git", "config")}
	if processStartGitConfigEnv != "" {
		invoking = append(invoking, processStartGitConfigEnv)
	}
	before := snapshotFiles(t, invoking)

	t.Setenv(DaemonTypeEnvVar, "managed")
	previousExit := workspaceGuardExit
	workspaceGuardExit = func(int) {}
	t.Cleanup(func() { workspaceGuardExit = previousExit })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Start returns as soon as it reaches the run loop
	_ = Start(ctx, StartOptions{BootstrapConfig: testBootstrapConfig(t.TempDir())})

	// The managed wiring really ran, and into the isolated config...
	out, err := exec.Command("git", "config", "--global", "--show-origin", "--get-all", gitCredentialHelperKey()).CombinedOutput()
	require.NoError(t, err, "managed Start must have installed the helper somewhere: %s", out)
	require.Contains(t, string(out), "git-credential")
	origin := strings.TrimPrefix(strings.Fields(string(out))[0], "file:")
	require.Equal(t, os.Getenv("GIT_CONFIG_GLOBAL"), origin, "helper written outside the isolated git config")

	// ...and the invoking user's config is byte-for-byte what it was.
	require.Equal(t, before, snapshotFiles(t, invoking), "a managed Start modified the invoking user's git config")
}

// snapshotFiles returns path -> contents, with absence recorded as a distinct
// value so creating a file is a change.
func snapshotFiles(t *testing.T, paths []string) map[string]string {
	t.Helper()
	snap := make(map[string]string, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		switch {
		case err == nil:
			snap[p] = "present:" + string(b)
		case os.IsNotExist(err):
			snap[p] = "absent"
		default:
			t.Fatalf("read %s: %v", p, err)
		}
	}
	return snap
}
