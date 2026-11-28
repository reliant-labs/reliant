// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// A long-running chat's directory is in use every hour (its briefing, its
// newest sub-agent), while the checkouts its finished sub-agents left in it
// sit idle for days. Each of those is stripped on its own clock: on one
// machine 107 of the 108 GiB in scratch sat in directories touched within
// the last day, and no pass stripped anything.
func TestScratch_IdleUnitsOfAnActiveDirectoryAreStripped(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	write(t, dir, "agent-old/app/package.json", "{}")
	write(t, dir, "agent-old/app/node_modules/x/index.js", "12345")
	write(t, dir, "agent-old/app/dist/main.js", "built")
	write(t, dir, "agent-new/app/package.json", "{}")
	write(t, dir, "agent-new/app/node_modules/x/index.js", "1")
	h.age(dir, 4*day)
	h.age(filepath.Join(dir, "agent-new"), time.Hour)
	write(t, dir, "briefing.md", "updated just now")

	e := h.entry(h.pass(), dir)
	require.Equal(t, ActionStripped, e.Action)
	require.ElementsMatch(t, []string{"agent-old/app/node_modules", "agent-old/app/dist"}, e.Removed)
	require.False(t, exists(filepath.Join(dir, "agent-old/app/node_modules")))
	require.True(t, exists(filepath.Join(dir, "agent-old/app/package.json")))
	require.True(t, exists(filepath.Join(dir, "agent-new/app/node_modules/x/index.js")), "a unit used an hour ago keeps its output")
	require.Contains(t, h.r.Records.Note(filepath.Join(dir, "agent-old/app")), "node_modules")
}

// A unit is held by its own use: a process in it, or recorded use of a path
// in it, or a git HEAD moved; and the directory's own use still counts for a
// target directly in it.
func TestScratch_UnitUseHoldsItsOutput(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	for _, u := range []string{"busy", "named", "top"} {
		write(t, dir, u+"/package.json", "{}")
		write(t, dir, u+"/node_modules/x/index.js", "1")
	}
	write(t, dir, "package.json", "{}")
	write(t, dir, "node_modules/x/index.js", "1")
	h.age(dir, 4*day)
	write(t, dir, "briefing.md", "updated just now")
	h.busy[filepath.Join(dir, "busy")] = true
	h.scratchUse[filepath.Join(dir, "named")] = h.now.Add(-time.Hour)

	e := h.entry(h.pass(), dir)
	require.Equal(t, []string{"top/node_modules"}, e.Removed)
	require.True(t, exists(filepath.Join(dir, "busy/node_modules")))
	require.True(t, exists(filepath.Join(dir, "named/node_modules")))
	require.True(t, exists(filepath.Join(dir, "node_modules")), "the directory itself is in use")
	require.Contains(t, e.Held, "busy: in use: process 42 has its cwd in "+filepath.Join(dir, "busy"))
}

// A linked worktree a finished sub-agent left in an active chat's directory:
// its ignored node_modules go once it is idle, and (with the machine-wide
// worktree reclaim off) the clean, pushed worktree itself.
func TestScratch_IdleWorktreeInAnActiveDirectory(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	main := pushedClone(t, t.TempDir(), filepath.Join(t.TempDir(), "main"))
	dir := h.chat("c1")
	wt := filepath.Join(dir, "agent", "wt-1")
	gitRun(t, main, "worktree", "add", "-q", "--detach", wt, "main")
	write(t, wt, "node_modules/x/index.js", "1")
	h.ageWorktree(wt, 3*day)
	write(t, dir, "briefing.md", "updated just now")

	h.r.WorktreeSweepOn = true
	e := h.entry(h.pass(), dir)
	require.Equal(t, []string{"agent/wt-1/node_modules"}, e.Removed)
	require.True(t, exists(wt))

	h.r.WorktreeSweepOn = false
	h.ageWorktree(wt, 3*day)
	e = h.entry(h.pass(), dir)
	require.Equal(t, []string{"agent/wt-1 (git worktree)"}, e.Removed)
	require.False(t, exists(wt))
}

// A finished sub-agent's directory in a chat directory still in use goes
// whole after 7 days on its own clock, with the clean, pushed clone it made;
// a sub-agent directory with unpushed work, or one used recently, or one
// named by recent work, stays, and so does anything not named for an agent.
// The chat's own tool calls are not its sub-agents' use.
func TestScratch_IdleSubAgentDirectoryGoesWhole(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	const (
		done     = "0b1f2c3d-4e5f-5a6b-8c7d-9e0f1a2b3c4d"
		unpushed = "1b1f2c3d-4e5f-5a6b-8c7d-9e0f1a2b3c4d"
		named    = "2b1f2c3d-4e5f-5a6b-8c7d-9e0f1a2b3c4d"
		working  = "3b1f2c3d-4e5f-5a6b-8c7d-9e0f1a2b3c4d"
	)
	write(t, dir, done+"/brief.md", "x")
	pushedClone(t, t.TempDir(), filepath.Join(dir, done, "repo"))
	clone := pushedClone(t, t.TempDir(), filepath.Join(dir, unpushed, "repo"))
	write(t, clone, "new.txt", "x")
	gitRun(t, clone, "add", ".")
	gitRun(t, clone, "commit", "-q", "-m", "local only")
	write(t, dir, named+"/brief.md", "x")
	write(t, dir, working+"/brief.md", "x")
	write(t, dir, "shared/notes.md", "x")
	h.age(dir, 9*day)
	h.age(filepath.Join(dir, working), time.Hour)
	h.scratchUse[filepath.Join(dir, named)] = h.now.Add(-time.Hour)
	h.chatUse[dir] = h.now.Add(-time.Minute)

	rep := h.pass()
	require.Equal(t, ActionRemoved, h.entry(rep, filepath.Join(dir, done)).Action)
	require.False(t, exists(filepath.Join(dir, done)))
	require.True(t, exists(clone))
	require.Contains(t, h.entry(rep, filepath.Join(dir, unpushed)).Held[0], "on no remote")
	require.True(t, exists(filepath.Join(dir, named, "brief.md")))
	require.True(t, exists(filepath.Join(dir, working, "brief.md")))
	require.True(t, exists(filepath.Join(dir, "shared/notes.md")))
	require.Equal(t, ActionActive, h.entry(rep, dir).Action)
}

// A directory too large to measure whole, or with a process in it, is still
// stripped unit by unit.
func TestScratch_InUseDirectoryStillStripsIdleUnits(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	dir := h.chat("c1")
	write(t, dir, "old/package.json", "{}")
	write(t, dir, "old/node_modules/x/index.js", "1")
	write(t, dir, "live/x.log", "1")
	h.age(dir, 9*day)
	h.busy[filepath.Join(dir, "live")] = true

	e := h.entry(h.pass(), dir)
	require.Equal(t, ActionStripped, e.Action)
	require.Equal(t, []string{"old/node_modules"}, e.Removed)
	require.True(t, exists(dir))
}

// Under pressure the strip floor is a day, for scratch units and worktree
// node_modules alike; the whole-directory floor stays.
func TestPressurePassShortensStripFloors(t *testing.T) {
	requireSafeRemoval(t)
	t.Parallel()
	h := newHarness(t)
	h.r.NodeModules = worktreereclaim.NodeModulesTargets
	dir := h.chat("c1")
	write(t, dir, "app/package.json", "{}")
	write(t, dir, "app/node_modules/x/index.js", "1")
	h.age(dir, 30*time.Hour)
	checkout := h.worktreeWithNodeModules("w1")
	h.ageWorktree(checkout, 30*time.Hour)

	rep := h.pass()
	require.Equal(t, ActionActive, h.entry(rep, dir).Action)
	require.True(t, exists(filepath.Join(checkout, "node_modules")))

	rep = h.r.Pass(context.Background(), false, TriggerPressure)
	require.Equal(t, "24h0m0s", rep.ScratchStripIdle)
	require.Equal(t, DefaultScratchRemoveIdle.String(), rep.ScratchRemoveIdle)
	require.Equal(t, ActionStripped, h.entry(rep, dir).Action)
	require.False(t, exists(filepath.Join(dir, "app/node_modules")))
	require.Equal(t, ActionStripped, h.entry(rep, checkout).Action)
	require.False(t, exists(filepath.Join(checkout, "node_modules")))
	require.True(t, exists(filepath.Join(checkout, ".env")))
	require.Equal(t, DefaultScratchStripIdle, h.r.floors().ScratchStripIdle, "the floors are the pass's only")
}

func TestNextPass(t *testing.T) {
	t.Parallel()
	start := harnessEpoch
	for _, tc := range []struct {
		name     string
		pressure bool
		last     time.Duration // since start; <0: none
		at       time.Duration
		trigger  string
		due      bool
	}{
		{"first scheduled waits", false, -1, 5 * time.Minute, TriggerScheduled, false},
		{"first scheduled", false, -1, firstPassDelay, TriggerScheduled, true},
		{"first under pressure", true, -1, pressureStartDelay, TriggerPressure, true},
		{"pressure repeats", true, 0, PressurePassInterval, TriggerPressure, true},
		{"pressure waits", true, 0, PressurePassInterval - time.Minute, TriggerScheduled, false},
		{"pressure after a scheduled pass", true, time.Hour, time.Hour + PressurePassInterval, TriggerPressure, true},
		{"scheduled interval", false, 0, PassInterval, TriggerScheduled, true},
		{"scheduled waits", false, 0, time.Hour, TriggerScheduled, false},
	} {
		var last time.Time
		if tc.last >= 0 {
			last = start.Add(tc.last)
		}
		trigger, due := nextPass(tc.pressure, start, last, start.Add(tc.at))
		require.Equal(t, tc.due, due, tc.name)
		if due {
			require.Equal(t, tc.trigger, trigger, tc.name)
		}
	}
}

// The pressure floors: a day for what is regenerable, the whole-directory
// floor unchanged.
func TestConfig_PressureFloors(t *testing.T) {
	t.Parallel()
	require.Equal(t, PressureStripIdle, Config{ScratchStripIdle: 48 * time.Hour, WorktreeNodeModulesIdle: 72 * time.Hour}.pressureFloors().ScratchStripIdle)
	require.Equal(t, PressureStripIdle, Config{ScratchStripIdle: 48 * time.Hour, WorktreeNodeModulesIdle: 72 * time.Hour}.pressureFloors().WorktreeNodeModulesIdle)
	require.Equal(t, 7*day, Config{ScratchRemoveIdle: 7 * day}.pressureFloors().ScratchRemoveIdle)
}

// A linked worktree with work only it holds keeps the directory due for
// removal whole: a capture into a local ref is not a durable receipt, so
// nothing captures it here; the machine-wide worktree reclaim proposes it.
func TestScratch_WholeRemovalHoldsForLinkedWorktreeWork(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	main := pushedClone(t, t.TempDir(), filepath.Join(t.TempDir(), "main"))
	dir := h.chat("c1")
	wt := filepath.Join(dir, "agent", "wt")
	gitRun(t, main, "worktree", "add", "-q", "-b", "feature", wt, "main")
	write(t, wt, "package.json", `{"edited": true}`)
	h.ageWorktree(wt, 8*day)
	h.age(dir, 8*day)

	e := h.entry(h.pass(), dir)
	require.True(t, exists(wt), "work only it holds keeps the directory")
	require.NotEmpty(t, e.Held)
	require.Contains(t, e.Held[0], "uncommitted changes")
}
