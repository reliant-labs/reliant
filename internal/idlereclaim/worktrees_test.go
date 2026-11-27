// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// ageWorktree ages a linked worktree and its git admin directory (its HEAD
// and reflog are use too).
func (h *harness) ageWorktree(checkout string, d time.Duration) {
	h.t.Helper()
	h.age(checkout, d)
	h.age(adminDir(checkout), d)
}

// worktreeWithNodeModules adds a linked worktree of a pushed clone under the
// worktree root, with an ignored node_modules and an uncommitted edit: the
// kind of worktree nothing reclaims.
func (h *harness) worktreeWithNodeModules(name string) (checkout string) {
	h.t.Helper()
	main := pushedClone(h.t, h.t.TempDir(), filepath.Join(h.t.TempDir(), "main"))
	checkout = filepath.Join(h.r.WorktreeRoot, "proj", name, "repo")
	gitRun(h.t, main, "worktree", "add", "-q", "-b", name, checkout, "main")
	write(h.t, checkout, "node_modules/x/index.js", "module.exports = 1")
	write(h.t, checkout, "package.json", `{"edited": true}`)
	write(h.t, checkout, ".env", "SECRET=1")
	return checkout
}

// A dirty worktree idle for 3 days loses its node_modules and nothing else:
// not its edit, not its other ignored files, not its branch.
func TestWorktrees_StripsOnlyNodeModulesFromIdleKeptWorktrees(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	h.r.NodeModules = worktreereclaim.NodeModulesTargets
	c := h.worktreeWithNodeModules("idle")
	h.ageWorktree(c, 4*day)

	e := h.entry(h.pass(), c)
	require.Equal(t, ActionStripped, e.Action)
	require.Equal(t, []string{"node_modules"}, e.Removed)
	require.False(t, exists(filepath.Join(c, "node_modules")))
	require.True(t, exists(filepath.Join(c, ".env")))
	require.Contains(t, gitRun(t, c, "status", "--porcelain"), "package.json", "the edit is still there")
	require.Contains(t, h.r.Records.Note(c), "node_modules")
}

// A worktree used within the threshold, or used by a process, keeps its
// node_modules; a recently used one is not even reported.
func TestWorktrees_ActiveOrBusyKeepNodeModules(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.r.NodeModules = worktreereclaim.NodeModulesTargets
	recent := h.worktreeWithNodeModules("recent")
	h.ageWorktree(recent, 4*day)
	write(t, recent, "src/new.go", "package src") // a file changed now
	busy := h.worktreeWithNodeModules("busy")
	h.ageWorktree(busy, 4*day)
	h.busy[busy] = true

	rep := h.pass()
	require.True(t, exists(filepath.Join(recent, "node_modules")))
	for _, e := range rep.Worktrees {
		require.NotEqual(t, recent, e.Path, "an active worktree is not reported")
	}
	require.Equal(t, ActionHeld, h.entry(rep, busy).Action)
	require.True(t, exists(filepath.Join(busy, "node_modules")))
}

// The daemon's record of work naming a path in the workspace is use.
func TestWorktrees_NamedUseCountsAsUse(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.r.NodeModules = worktreereclaim.NodeModulesTargets
	c := h.worktreeWithNodeModules("used")
	h.ageWorktree(c, 4*day)
	h.r.WorktreeUse = func(p string) time.Time {
		if p == c {
			return h.now.Add(-time.Hour)
		}
		return time.Time{}
	}
	h.pass()
	require.True(t, exists(filepath.Join(c, "node_modules")))
}

// Discovery finds linked worktrees, never descends one, and skips dot
// directories (the worktree reclaim's quarantine, the trash).
func TestLinkedCheckouts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "proj/ws-1/repo/.git", "gitdir: /x/.git/worktrees/repo\n")
	write(t, root, "proj/ws-1/repo/sub/.git", "gitdir: /y\n")
	write(t, root, "flat/.git", "gitdir: /z\n")
	write(t, root, ".reclaim/ws/repo/.git", "gitdir: /q\n")
	write(t, root, "full-clone/.git/HEAD", "ref: refs/heads/main\n")
	require.Equal(t, []string{filepath.Join(root, "flat"), filepath.Join(root, "proj/ws-1/repo")}, linkedCheckouts(root))
}
