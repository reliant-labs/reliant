// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const shaA = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
const shaB = "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432"

// HEAD is read from the git directory as files, without running git: the
// watcher reads it every poll. A clone, a packed ref, a linked worktree (a
// .git file whose refs live in the main repository) and a detached HEAD.
func TestReadRepoHeads(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// The project root: a clone on main, origin's default main, loose ref.
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, ".git", "refs", "heads", "main"), shaA+"\n")
	writeFile(t, filepath.Join(root, ".git", "refs", "remotes", "origin", "HEAD"), "ref: refs/remotes/origin/main\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".reliant"), 0o755))
	// control-plane: a linked worktree on feat/x whose ref is packed in the
	// main repository.
	main := filepath.Join(t.TempDir(), "cp.git")
	wtGit := filepath.Join(main, "worktrees", "cp")
	writeFile(t, filepath.Join(wtGit, "HEAD"), "ref: refs/heads/feat/x\n")
	writeFile(t, filepath.Join(wtGit, "commondir"), "../..\n")
	writeFile(t, filepath.Join(main, "packed-refs"), "# pack-refs with: peeled\n"+shaB+" refs/heads/feat/x\n^"+shaA+"\n")
	writeFile(t, filepath.Join(main, "refs", "remotes", "origin", "HEAD"), "ref: refs/remotes/origin/main\n")
	cp := filepath.Join(root, "control-plane")
	writeFile(t, filepath.Join(cp, ".git"), "gitdir: "+wtGit+"\n")
	require.NoError(t, os.MkdirAll(filepath.Join(cp, ".reliant", "workflows"), 0o755))
	// forge: detached HEAD.
	forge := filepath.Join(root, "forge")
	writeFile(t, filepath.Join(forge, ".git", "HEAD"), shaB+"\n")
	require.NoError(t, os.MkdirAll(filepath.Join(forge, ".reliant"), 0o755))
	// docs: a repo with no .reliant directory is not reported.
	writeFile(t, filepath.Join(root, "docs", ".git", "HEAD"), "ref: refs/heads/main\n")

	heads := readRepoHeads(root, []string{"forge", "control-plane", "docs"})
	require.Len(t, heads, 3)
	assert.Equal(t, repoHead{repo: "", dir: root, branch: "main", commit: shaA, defaultBranch: "main"}, heads[0])
	assert.Equal(t, repoHead{repo: "control-plane", dir: cp, branch: "feat/x", commit: shaB, defaultBranch: "main"}, heads[1])
	assert.Equal(t, repoHead{repo: "forge", dir: forge, commit: shaB}, heads[2])

	// A commit moves the snapshot's version, so the watcher sends it.
	before := string(headsVersion(heads))
	writeFile(t, filepath.Join(root, ".git", "refs", "heads", "main"), shaB+"\n")
	assert.NotEqual(t, before, string(headsVersion(readRepoHeads(root, []string{"forge", "control-plane", "docs"}))))
}

// Dirty is .reliant's own state: an untracked or modified file under it, and
// nothing else in the repo.
func TestRepoStatesReportsDirtyReliantDir(t *testing.T) {
	testkit.Slow(t, "runs git init, commit and status in a temp repo")
	t.Parallel()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, ".reliant", "workflows", "release.yaml"), "name: release\n")
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ctx := context.Background()
	// The version as the snapshot computes it: HEAD, the index and the
	// .reliant files' stats.
	states := func() (bool, string) {
		t.Helper()
		s := repoStates(ctx, dir, readRepoHeads(dir, nil))
		require.Len(t, s, 1)
		require.Empty(t, s[0].GetError())
		return s[0].GetDirty(), s[0].GetBranch()
	}
	dirty, branch := states()
	assert.False(t, dirty)
	assert.Equal(t, "main", branch)

	writeFile(t, filepath.Join(dir, "README.md"), "outside .reliant\n")
	dirty, _ = states()
	assert.False(t, dirty, "a change outside .reliant is not what syncs")

	release := filepath.Join(dir, ".reliant", "workflows", "release.yaml")
	writeFile(t, release, "name: release\ndescription: edited\n")
	dirty, _ = states()
	assert.True(t, dirty)

	// Stage the edit, then put the file back as committed: the synced bytes
	// and HEAD are the clean ones again, but the index is not.
	git("add", release)
	writeFile(t, release, "name: release\n")
	dirty, _ = states()
	assert.True(t, dirty, "a staged change is uncommitted, though the file matches HEAD")
	git("reset", "-q", release)
	dirty, _ = states()
	assert.False(t, dirty)

	// An untracked file under .reliant that is not synced is still dirty.
	writeFile(t, filepath.Join(dir, ".reliant", "notes.txt"), "todo\n")
	dirty, _ = states()
	assert.True(t, dirty)
}

// A failed dirty read is retried, with backoff, and not kept: once git
// recovers, an unchanged checkout reports its real state, and the snapshot's
// version moves so the watcher sends it.
func TestRepoStatesRetriesAFailedDirtyRead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, ".git", "refs", "heads", "main"), shaA+"\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".reliant"), 0o755))

	calls, fail := 0, true
	origFn, origMin := reliantDirDirtyFn, repoDirtyRetryMin
	reliantDirDirtyFn = func(context.Context, string) (bool, error) {
		calls++
		if fail {
			return false, errors.New("git status .reliant: signal: killed")
		}
		return true, nil
	}
	repoDirtyRetryMin = time.Hour
	t.Cleanup(func() { reliantDirDirtyFn, repoDirtyRetryMin = origFn, origMin })

	ctx := context.Background()
	heads := readRepoHeads(root, nil)
	failed := repoStates(ctx, root, heads)
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].GetError(), "signal: killed")
	assert.False(t, failed[0].GetDirty())
	repoStates(ctx, root, heads)
	assert.Equal(t, 1, calls, "backing off: no retry before retryAt")

	// The retry is due, and git works again.
	repoDirtyCache.Lock()
	e := repoDirtyCache.byProject[root]
	e.retryAt = time.Now().Add(-time.Second)
	repoDirtyCache.byProject[root] = e
	repoDirtyCache.Unlock()
	fail = false
	recovered := repoStates(ctx, root, heads)
	assert.Equal(t, 2, calls)
	assert.Empty(t, recovered[0].GetError())
	assert.True(t, recovered[0].GetDirty())
	assert.NotEqual(t, string(statesVersion(failed)), string(statesVersion(recovered)), "the recovery changes the snapshot's version")

	repoStates(ctx, root, heads)
	assert.Equal(t, 2, calls, "a good read is kept")
}
