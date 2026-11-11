// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The reclaim tombstone: placed where anything working in the workspace sees
// it, never in git, removed on cancel, and required for the archived
// worktree's removal.

// workspace creates a multi-repo workspace the way reliant lays one out:
// <root>/proj/<id>/ with the checkout at Rel "reliant".
func (f *fixture) workspace(id, branch string) Worktree {
	f.t.Helper()
	ws := filepath.Join(f.root, "proj", id)
	path := filepath.Join(ws, "reliant")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		f.t.Fatal(err)
	}
	run(f.t, f.project, "worktree", "add", "-q", "-b", branch, path, "main")
	if err := LockCheckout(context.Background(), path, id); err != nil {
		f.t.Fatal(err)
	}
	return Worktree{ID: id, Path: ws, State: StateActive, BaseBranch: "main",
		Checkouts: []Checkout{{RepoPath: f.project, Rel: "reliant"}}}
}

func (f *fixture) active(w Worktree) Result {
	f.t.Helper()
	w.State, w.Fence = StateActive, ""
	return f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
}

func TestTombstone_MarkPlacesItWhereAnAgentLooksAndGitNeverSees(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a linked worktree, marked, unmarked and probed (~30 git processes)")
	f := newFixture(t)
	w := f.workspace("marked", "feat/marked")
	checkout := filepath.Join(w.Path, "reliant")

	// Before any mark: the capability, and no tombstone.
	res := f.active(w)
	if res.Outcome != OutcomeLocked || res.Tombstone == nil || res.Tombstone.MarkID != "" {
		t.Fatalf("unmarked: got %+v, want locked with an empty tombstone report", res)
	}

	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	w.Mark = &ReclaimMark{ID: "m-1", ReclaimAt: at}
	w.ProbeLanded = true
	res = f.active(w)
	if res.Outcome != OutcomeLocked || res.Tombstone == nil || res.Tombstone.MarkID != "m-1" {
		t.Fatalf("marked: got %+v, want locked carrying m-1", res)
	}
	if !res.Landed {
		t.Fatalf("the tombstone must not make a landed checkout look unlanded or dirty: %+v", res)
	}
	body, err := os.ReadFile(filepath.Join(checkout, ".reliant", TombstoneFile))
	if err != nil || !strings.Contains(string(body), "2026-11-12T09:00:00Z") || !strings.Contains(string(body), "delete `.reliant/RECLAIM_PENDING`") {
		t.Fatalf("tombstone %q (%v)", body, err)
	}
	if out := run(t, checkout, "status", "--porcelain", "--ignored=no"); strings.TrimSpace(out) != "" {
		t.Fatalf("git sees the tombstone:\n%s", out)
	}
	if out := run(t, checkout, "add", "-A", "--dry-run"); strings.Contains(out, TombstoneFile) {
		t.Fatalf("`git add -A` would commit it:\n%s", out)
	}
	memory, err := os.ReadFile(filepath.Join(w.Path, memoryFile))
	if err != nil || !strings.Contains(string(memory), "marked to be reclaimed at 2026-11-12T09:00:00Z") {
		t.Fatalf("workspace-root reliant.md %q (%v)", memory, err)
	}

	// Re-sending the same mark changes nothing on disk.
	before, _ := os.Stat(filepath.Join(checkout, ".reliant", TombstoneFile))
	if res = f.active(w); res.Tombstone == nil || res.Tombstone.MarkID != "m-1" {
		t.Fatalf("re-marked: %+v", res)
	}
	after, _ := os.Stat(filepath.Join(checkout, ".reliant", TombstoneFile))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("re-sending a mark rewrote the tombstone")
	}

	// Unmarking another mark leaves it; unmarking this one removes it all.
	w.Mark, w.Unmark = nil, "m-other"
	if res = f.active(w); res.Tombstone.MarkID != "m-1" {
		t.Fatalf("an unmark for another mark removed it: %+v", res)
	}
	w.Unmark = "m-1"
	if res = f.active(w); res.Outcome != OutcomeLocked || res.Tombstone == nil || res.Tombstone.MarkID != "" {
		t.Fatalf("unmarked: %+v", res)
	}
	if exists(filepath.Join(checkout, ".reliant")) || exists(filepath.Join(w.Path, memoryFile)) {
		t.Fatal("unmarking left the tombstone or the memory file behind")
	}
}

func TestTombstone_MemoryLineKeepsWhatTheUserWrote(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), memoryFile)
	mine := "# My notes\n\nkeep this\n"
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	m := ReclaimMark{ID: "m-1", ReclaimAt: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	if err := writeMemoryLine(path, m); err != nil {
		t.Fatal(err)
	}
	m2 := ReclaimMark{ID: "m-2", ReclaimAt: m.ReclaimAt.Add(time.Hour)}
	if err := writeMemoryLine(path, m2); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), memoryMarkerOpen) != 1 || !strings.Contains(string(b), "m-2") || !strings.Contains(string(b), mine) {
		t.Fatalf("after two marks:\n%s", b)
	}
	if err := removeMemoryLine(path, "m-1"); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(path); !strings.Contains(string(b), "m-2") {
		t.Fatalf("removing a stale mark's line removed the current one:\n%s", b)
	}
	if err := removeMemoryLine(path, "m-2"); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(path); string(b) != mine {
		t.Fatalf("after unmarking: %q, want the user's file as it was", b)
	}
}

func TestTombstone_ArchivedUnderAMarkIsRemovedOnlyWhileItStands(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a marked worktree archived and settled (~40 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.workspace("reclaimed", "feat/reclaimed")
	w.Mark = &ReclaimMark{ID: "m-1", ReclaimAt: time.Now()}
	if res := f.active(w); res.Tombstone == nil || res.Tombstone.MarkID != "m-1" {
		t.Fatalf("mark: %+v", res)
	}
	w.Mark = nil

	// Someone deleted the tombstone: kept.
	if err := os.Remove(filepath.Join(w.Path, "reliant", ".reliant", TombstoneFile)); err != nil {
		t.Fatal(err)
	}
	archived := w
	archived.State, archived.Fence, archived.ReclaimMark = StateArchived, "100", "m-1"
	if res := f.one(archived); res.Outcome != OutcomeHeld || res.Reason != ReasonMarked {
		t.Fatalf("tombstone removed: got %+v, want held %s", res, ReasonMarked)
	}
	if !exists(w.Path) {
		t.Fatal("removed a worktree whose tombstone was deleted")
	}

	// A different mark's tombstone does not authorize this one either.
	w.Mark = &ReclaimMark{ID: "m-2", ReclaimAt: time.Now()}
	f.active(w)
	if res := f.one(archived); res.Outcome != OutcomeHeld || res.Reason != ReasonMarked {
		t.Fatalf("another mark's tombstone: got %+v, want held %s", res, ReasonMarked)
	}

	// The archive's own mark, still standing: removed.
	archived.ReclaimMark = "m-2"
	if res := f.one(archived); res.Outcome != OutcomeRemoved {
		t.Fatalf("tombstone standing: got %+v, want removed", res)
	}
}

func TestTombstone_CommandsTouchOnlyActiveWorktrees(t *testing.T) {
	t.Parallel()
	r := &Reclaimer{Root: t.TempDir()}
	m := &ReclaimMark{ID: "m-1", ReclaimAt: time.Now()}
	got := r.Mark(context.Background(), Request{Worktrees: []Worktree{
		{ID: "archived", Path: filepath.Join(r.Root, "a"), State: StateArchived, Fence: "1", Mark: m},
		{ID: "unmarked", Path: filepath.Join(r.Root, "b"), State: StateActive},
		{ID: "removing", Path: filepath.Join(r.Root, "c"), State: StateActive, Remove: true, Mark: m},
	}}).Results
	if len(got) != 3 {
		t.Fatalf("got %d results, want one per worktree", len(got))
	}
	for i, id := range []string{"archived", "unmarked", "removing"} {
		if got[i].ID != id || got[i].Outcome != OutcomeError {
			t.Errorf("%s: got %+v, want refused", id, got[i])
		}
	}
	// One that qualifies is settled like any active reconcile, in order.
	got = r.Unmark(context.Background(), Request{Worktrees: []Worktree{
		{ID: "archived", Path: filepath.Join(r.Root, "a"), State: StateArchived, Fence: "1"},
		{ID: "gone", Path: filepath.Join(r.Root, "gone"), State: StateActive},
	}}).Results
	if got[0].Outcome != OutcomeError || got[1].ID != "gone" || got[1].Outcome != OutcomeGone {
		t.Fatalf("got %+v, want the archived refused and the active one settled", got)
	}
}
