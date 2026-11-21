// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Releases before on-demand credentials wrote `credential.helper store` into
// every clone they made. git_publish refuses any helper in a checkout's own
// config, so those clones (and every worktree of them) could never publish.

// managedRoot is a directory under managedProjectsDir, where a daemon clones.
func managedRoot(t *testing.T) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(managedProjectsDir, 0o755))
	dir, err := os.MkdirTemp(managedProjectsDir, "clone-helper-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// localHelpers returns the credential.helper values in dir's repository config.
func localHelpers(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "config", "--local", "--get-all", "credential.helper").Output()
	if err != nil {
		return nil // exit 1: not set
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func TestHealCloneCredentialHelper(t *testing.T) {
	t.Parallel()
	needGit(t)
	for _, tc := range []struct {
		name    string
		helpers []string
		removed bool
	}{
		{name: "the daemon's store is removed", helpers: []string{"store"}, removed: true},
		{name: "a user's own helper is kept", helpers: []string{"osxkeychain"}},
		{name: "store beside a user's helper is kept", helpers: []string{"store", "!my-helper"}},
		{name: "no helper is a no-op"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "clone")
			pubGit(t, filepath.Dir(dir), "init", "-q", dir)
			for _, h := range tc.helpers {
				pubGit(t, dir, "config", "--add", "credential.helper", h)
			}
			env := isolatedGitEnv()

			assert.Equal(t, tc.removed, healCloneCredentialHelper(context.Background(), dir, env))
			if tc.removed {
				assert.Empty(t, localHelpers(t, dir))
			} else {
				assert.Equal(t, tc.helpers, nilIfEmpty(localHelpers(t, dir)))
			}
			assert.False(t, healCloneCredentialHelper(context.Background(), dir, env), "a second run finds nothing")
		})
	}
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func TestGitPublish_CloneCredentialHelperAnOldDaemonWrote(t *testing.T) {
	t.Parallel()
	needGit(t)
	for _, tc := range []struct {
		name string
		// managed puts the clone under managedProjectsDir, where a daemon clones.
		managed bool
		// viaWorktree publishes from a worktree of the clone, which shares its config.
		viaWorktree bool
		helpers     []string
		wantPushed  bool
	}{
		{name: "the daemon's store in a managed clone is removed", managed: true, helpers: []string{"store"}, wantPushed: true},
		{name: "a worktree of such a clone publishes", managed: true, viaWorktree: true, helpers: []string{"store"}, wantPushed: true},
		{name: "a user's own helper is kept and named", managed: true, helpers: []string{"!my-own-helper"}},
		{name: "store beside a user's helper is kept", managed: true, helpers: []string{"store", "!my-own-helper"}},
		{name: "store outside the managed projects is the user's", helpers: []string{"store"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			allowFileRemotes(t)
			root := t.TempDir()
			if tc.managed {
				root = managedRoot(t)
			}
			f := newPublishFixtureIn(t, root)
			for _, h := range tc.helpers {
				pubGit(t, f.clone, "config", "--add", "credential.helper", h)
			}
			checkout := f.clone
			if tc.viaWorktree {
				// Outside the managed projects, as a daemon's worktrees are: what
				// counts is the repository the worktree belongs to.
				checkout = filepath.Join(t.TempDir(), "wt")
				pubGit(t, f.clone, "worktree", "add", "-q", "-b", "wt", checkout)
			}

			for i, branch := range []string{"autopilot/one", "autopilot/two"} {
				require.NoError(t, os.WriteFile(filepath.Join(checkout, branch[len("autopilot/"):]+".txt"), []byte("x\n"), 0o644))
				prep := prepare(t, checkout, "change")
				req := f.pushReq(branch, prep.HeadSHA)
				req.Worktree = checkout
				res := push(t, req)

				if tc.wantPushed {
					require.True(t, res.Pushed, "run %d: %s", i, res.Refusal)
					assert.Equal(t, prep.HeadSHA, remoteHead(t, f, branch))
					assert.Empty(t, localHelpers(t, f.clone), "the daemon's helper is gone")
					continue
				}
				assert.False(t, res.Pushed)
				assert.Empty(t, remoteHead(t, f, branch))
				assert.Equal(t, tc.helpers, localHelpers(t, f.clone), "a helper the daemon did not write is kept")
				// The refusal names the key, the file and the command that removes
				// it — and never the value.
				assert.Contains(t, res.Refusal, "sets credential.helper in ")
				assert.Contains(t, res.Refusal, filepath.Join(".git", "config"))
				assert.Contains(t, res.Refusal, "--unset-all 'credential.helper'")
				// ("store" is also in the temp paths named after these subtests.)
				assert.NotContains(t, res.Refusal, "my-own-helper")
			}
		})
	}
}
