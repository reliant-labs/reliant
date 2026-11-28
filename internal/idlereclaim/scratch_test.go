// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A directory used within the strip age is left alone entirely.
func TestScratch_ActiveDirectoryIsUntouched(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	write(t, dir, "app/package.json", "{}")
	write(t, dir, "app/node_modules/x/index.js", "module.exports = 1")
	h.age(dir, 47*time.Hour)

	e := h.entry(h.pass(), dir)
	require.Equal(t, ActionActive, e.Action)
	require.True(t, exists(filepath.Join(dir, "app/node_modules")))
}

// After 48h unused: node_modules and a build output beside its manifest go;
// the briefing, an "out" with no manifest beside it, and the directory stay.
// The removal is recorded for the failed-command note, and it does not count
// as use: the directory's mtime is put back.
func TestScratch_StripRemovesOnlyRegenerableOutput(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	write(t, dir, "app/package.json", "{}")
	write(t, dir, "app/node_modules/x/index.js", "module.exports = 1")
	write(t, dir, "app/dist/main.js", "built")
	write(t, dir, "out/report.md", "an agent's output, not a build")
	h.age(dir, 3*day)

	e := h.entry(h.pass(), dir)
	require.Equal(t, ActionStripped, e.Action)
	require.ElementsMatch(t, []string{"app/node_modules", "app/dist"}, e.Removed)
	require.Positive(t, e.Bytes)
	require.False(t, exists(filepath.Join(dir, "app/node_modules")))
	require.False(t, exists(filepath.Join(dir, "app/dist")))
	require.True(t, exists(filepath.Join(dir, "briefing.md")))
	require.True(t, exists(filepath.Join(dir, "out/report.md")))
	require.True(t, exists(filepath.Join(dir, "app/package.json")))
	require.False(t, exists(filepath.Join(h.r.ScratchRoot, TrashDir)), "the trash is emptied by the pass")

	info, err := os.Stat(filepath.Join(dir, "app"))
	require.NoError(t, err)
	require.WithinDuration(t, h.now.Add(-3*day), info.ModTime(), time.Second, "the strip is not use of the directory")
	require.Contains(t, h.r.Records.Note(filepath.Join(dir, "app")), "node_modules")
}

// After 7 days unused the whole directory goes.
func TestScratch_RemovesTheWholeDirectoryAfterSevenDays(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	write(t, dir, "notes/a.md", "a")
	h.age(dir, 8*day)

	e := h.entry(h.pass(), dir)
	require.Equal(t, ActionRemoved, e.Action)
	require.Equal(t, []string{"."}, e.Removed)
	require.False(t, exists(dir))
}

// A .keep file at the top keeps the directory and everything in it.
func TestScratch_KeepFileKeepsEverything(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	write(t, dir, KeepFile, "")
	write(t, dir, "app/package.json", "{}")
	write(t, dir, "app/node_modules/x/index.js", "1")
	h.age(dir, 30*day)

	e := h.entry(h.pass(), dir)
	require.Equal(t, ActionKept, e.Action)
	require.True(t, exists(filepath.Join(dir, "app/node_modules/x/index.js")))
}

// Reading a briefing leaves no mtime; the daemon's record of work naming a
// path in the directory is use all the same.
func TestScratch_NamedUseCountsAsUse(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	h.age(dir, 8*day)
	h.scratchUse[dir] = h.now.Add(-time.Hour)

	require.Equal(t, ActionActive, h.entry(h.pass(), dir).Action)
	require.True(t, exists(dir))
}

// A chat that is still making tool calls keeps its directory whole past 7
// days; the strip still runs on the files' own clock.
func TestScratch_ChatActivityDefersWholeRemovalOnly(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	write(t, dir, "app/package.json", "{}")
	write(t, dir, "app/node_modules/x/index.js", "1")
	h.age(dir, 8*day)
	h.chatUse[dir] = h.now.Add(-2 * time.Hour)

	e := h.entry(h.pass(), dir)
	require.Equal(t, ActionStripped, e.Action)
	require.True(t, exists(filepath.Join(dir, "briefing.md")))
	require.False(t, exists(filepath.Join(dir, "app/node_modules")))
	require.Contains(t, e.Held[0], "its chat made a tool call")
}

// A live process using the directory holds it, at any age.
func TestScratch_ProcessUseHolds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	h.age(dir, 30*day)
	h.busy[filepath.Join(dir)] = true

	e := h.entry(h.pass(), dir)
	require.Equal(t, ActionHeld, e.Action)
	require.Contains(t, e.Reason, "in use")
	require.True(t, exists(dir))
}

// A refused admission lease (daemon work running there) removes nothing.
func TestScratch_RefusedLeaseRemovesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	write(t, dir, "app/package.json", "{}")
	write(t, dir, "app/node_modules/x/index.js", "1")
	h.age(dir, 30*day)
	h.refuse = true

	e := h.entry(h.pass(), dir)
	require.Equal(t, ActionHeld, e.Action)
	require.True(t, exists(filepath.Join(dir, "app/node_modules/x/index.js")))
}

// Dry-run reports what it would remove, with the bytes, and removes nothing.
func TestScratch_DryRunRemovesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	old := h.chat("old")
	h.age(old, 8*day)
	stale := h.chat("stale")
	write(t, stale, "app/package.json", "{}")
	write(t, stale, "app/node_modules/x/index.js", "12345")
	h.age(stale, 3*day)

	rep := h.r.Pass(context.Background(), true, "test")
	require.True(t, rep.DryRun)
	require.Equal(t, ActionRemoved, h.entry(rep, old).Action)
	s := h.entry(rep, stale)
	require.Equal(t, ActionStripped, s.Action)
	require.True(t, s.DryRun)
	require.EqualValues(t, 5, s.Bytes)
	require.True(t, exists(old))
	require.True(t, exists(filepath.Join(stale, "app/node_modules/x/index.js")))
	require.Equal(t, "", h.r.Records.Note(filepath.Join(stale, "app")), "nothing was removed, so nothing is noted")
}

// A dot directory at the scratch root (the trash) is never a chat's.
func TestScratch_DotDirectoriesAreNotChats(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	hidden := filepath.Join(h.r.ScratchRoot, ".hidden")
	write(t, hidden, "x", "x")
	h.age(hidden, 30*day)
	rep := h.pass()
	require.Empty(t, rep.Scratch)
	require.True(t, exists(hidden))
}

// A git repository with work only it holds keeps the directory from being
// removed whole: uncommitted changes, or commits on no remote.
func TestScratch_UniqueGitWorkHoldsWholeRemoval(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	dirty := h.chat("dirty")
	clone := pushedClone(t, t.TempDir(), filepath.Join(dirty, "repo"))
	write(t, clone, "package.json", `{"edited": true}`)
	h.age(dirty, 8*day)

	local := h.chat("local")
	clone2 := pushedClone(t, t.TempDir(), filepath.Join(local, "repo"))
	write(t, clone2, "new.txt", "x")
	gitRun(t, clone2, "add", ".")
	gitRun(t, clone2, "commit", "-q", "-m", "local only")
	h.age(local, 8*day)

	clean := h.chat("clean")
	pushedClone(t, t.TempDir(), filepath.Join(clean, "repo"))
	h.age(clean, 8*day)

	rep := h.pass()
	require.True(t, exists(dirty))
	require.Contains(t, h.entry(rep, dirty).Held[0], "uncommitted changes")
	require.True(t, exists(local))
	require.Contains(t, h.entry(rep, local).Held[0], "on no remote")
	require.Equal(t, ActionRemoved, h.entry(rep, clean).Action, "a clean, pushed clone exists on its remote")
	require.False(t, exists(clean))
}

// After 48h, a linked worktree in a scratch directory that is clean and
// pushed is removed with `git worktree remove`; a dirty one stays. Neither is
// touched when the machine-wide worktree reclaim owns worktrees (mode on).
func TestScratch_StripRemovesCleanPushedWorktrees(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	main := pushedClone(t, t.TempDir(), filepath.Join(t.TempDir(), "main"))
	dir := h.chat("c1")
	clean := filepath.Join(dir, "wt-clean")
	gitRun(t, main, "worktree", "add", "-q", "--detach", clean, "main")
	write(t, clean, "node_modules/x/index.js", "1")
	dirty := filepath.Join(dir, "wt-dirty")
	gitRun(t, main, "worktree", "add", "-q", "--detach", dirty, "main")
	write(t, dirty, "package.json", `{"edited": true}`)
	h.age(dir, 3*day)

	h.r.WorktreeSweepOn = true
	e := h.entry(h.pass(), dir)
	require.True(t, exists(clean), "mode on: the worktree reclaim's to remove")
	require.Equal(t, []string{"wt-clean/node_modules"}, e.Removed)

	h.r.WorktreeSweepOn = false
	h.age(dir, 3*day)
	e = h.entry(h.pass(), dir)
	require.Contains(t, e.Removed, "wt-clean (git worktree)")
	require.False(t, exists(clean))
	require.True(t, exists(filepath.Join(dirty, "package.json")))
	require.NotContains(t, gitRun(t, main, "worktree", "list"), "wt-clean")
}
