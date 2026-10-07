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

// Regression tests ported from the third review of #574 (probes Q1-Q3, R1-R3, P9).
// Assertions follow the decided rules: quarantine to a fresh unique path, never
// move back into an existing path, never report gone while a quarantine exists;
// apply a snapshot only onto the commit it was taken on.

// Q1: the daemon dies between the quarantine move and the removal (or the
// put-back). Simulated by doing exactly what removeOne does up to the move,
// then running the next ordinary pass.
func TestReview3_Q1_CrashMidQuarantine(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("q1", "feat/q1")
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.Outcome != OutcomeClaimed {
		t.Fatalf("phase one: %+v", got)
	}
	ctx := context.Background()
	info, _ := inspectLock(ctx, w.Path)
	q := f.r.newQuarantinePath(w, w.Path)
	_ = os.MkdirAll(filepath.Dir(q), 0o755)
	run(t, f.project, "worktree", "unlock", w.Path)
	run(t, f.project, "worktree", "move", w.Path, q)
	_ = setLock(ctx, lockInfo{CommonDir: info.CommonDir}, q, ArchivedLockReason(w.ID, w.Fence))
	// --- crash here; daemon restarts; next pass ---
	w.Checkouts = []Checkout{{RepoPath: f.project}}
	next := f.r.Reconcile(ctx, Request{Worktrees: []Worktree{w}}).Results[0]
	if next.Outcome != OutcomeHeld || next.Reason != ReasonQuarantined || !strings.Contains(next.Detail, ".reclaim") {
		t.Errorf("a quarantined checkout must be held as %q naming its path, got %+v", ReasonQuarantined, next)
	}
	qi, _ := inspectLock(ctx, q)
	t.Logf("next pass: %+v; quarantined copy exists=%v locked=%v reason=%q", next, exists(q), qi.Locked, qi.Reason)
	// Can the user's Restore (recreate) bring it back?
	_, addErr := gitOut(f.project, "worktree", "add", w.Path, "feat/q1")
	t.Logf("recreate's `git worktree add` at the original path: err=%v", addErr)
	if next.Outcome == OutcomeGone {
		t.Errorf("STRANDED: the next pass reports %q (server records directory_deleted) while the tree sits in %s; restore err=%v", next.Outcome, q, addErr)
	}
	if !exists(q) {
		t.Errorf("the quarantined copy was deleted")
	}
}

// Q2: the put-back move fails because the original path was re-created while
// the checkout sat in quarantine. Is the stranded copy still locked?
func TestReview3_Q2_PutBackFailureLeavesItUnlocked(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("q2", "feat/q2")
	calls := 0
	f.r.Cwds = func(context.Context) ([]string, error) {
		calls++
		q := ""
		if dirs := quarantineDirs(f, w.ID); len(dirs) > 0 {
			q = dirs[0]
		}
		if _, err := os.Stat(w.Path); os.IsNotExist(err) && q != "" {
			// something re-creates the original path, and a process is inside q
			write(t, filepath.Join(w.Path, "recreated-by-someone.txt"), "x")
			return []string{q}, nil
		}
		return nil, nil
	}
	got := f.one(w)
	q := ""
	if dirs := quarantineDirs(f, w.ID); len(dirs) > 0 {
		q = dirs[0]
	}
	if q == "" {
		t.Fatalf("nothing in quarantine: %+v", got)
	}
	qi, _ := inspectLock(context.Background(), q)
	t.Logf("result: %+v; q exists=%v locked=%v reason=%q", got, exists(q), qi.Locked, qi.Reason)
	if exists(q) && !qi.Locked {
		t.Errorf("STRANDED UNLOCKED: %s left in quarantine with no lock (forge's reaper no longer skips it)", q)
	}
	if got.Outcome != OutcomeHeld || got.Reason != ReasonQuarantined {
		t.Errorf("want held/quarantined, got %+v", got)
	}
	if exists(filepath.Join(w.Path, w.ID)) {
		t.Errorf("the checkout was moved INSIDE the re-created directory")
	}
}

// Q3: a workspace whose ROOT is a checkout and which holds a nested checkout
// (root repo "" + nested repo "web"). Deepest-first removal takes the nested
// one out, then the root's quarantine re-scan expects 1+len(nested) repos.
func TestReview3_Q3_RootCheckoutWithNestedCheckout(t *testing.T) {
	f := newFixture(t)
	f.idle()
	ignore(t, f, "web/\n")
	ws := filepath.Join(f.root, "proj", "q3")
	run(t, f.project, "worktree", "add", "-q", "-b", "feat/q3", ws, "main")
	_ = LockCheckout(context.Background(), ws, "q3")
	base := filepath.Dir(f.project)
	webRepo := filepath.Join(base, "webrepo")
	run(t, base, "init", "-q", "-b", "main", webRepo)
	write(t, filepath.Join(webRepo, "w.txt"), "w\n")
	run(t, webRepo, "add", ".")
	run(t, webRepo, "commit", "-q", "-m", "w")
	webOrigin := filepath.Join(base, "web-origin.git")
	run(t, base, "init", "-q", "--bare", "-b", "main", webOrigin)
	run(t, webRepo, "remote", "add", "origin", webOrigin)
	run(t, webRepo, "push", "-q", "origin", "main")
	run(t, webRepo, "worktree", "add", "-q", "-b", "feat/q3w", filepath.Join(ws, "web"), "main")
	_ = LockCheckout(context.Background(), filepath.Join(ws, "web"), "q3")
	w := Worktree{ID: "q3", Path: ws, State: StateArchived, BaseBranch: "main", Fence: "9"}
	got := f.one(w)
	t.Logf("result: %+v; root exists=%v web exists=%v", got, exists(filepath.Join(ws, ".git")), exists(filepath.Join(ws, "web", ".git")))
	if got.Outcome != OutcomeRemoved {
		t.Errorf("a clean root+nested workspace is not removed: %s/%s %q (web removed=%v)", got.Outcome, got.Reason, got.Detail, !exists(filepath.Join(ws, "web", ".git")))
	}
}

// R1: restore applies a snapshot over a HEAD that has moved past the snapshot's
// parent. LatestSnapshot falls back to every ref in the repo when the row has
// none (cmd_worktree.go snapshotRefsFor -> nil), so a snapshot from an EARLIER
// clean-up is applied again on a later restore.
func TestReview3_R1_StaleSnapshotReappliedOverNewerCommits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.worktree("r1", "feat/r1")
	write(t, filepath.Join(w.Path, "a.txt"), "edited in lifecycle 1\n")
	got := f.confirmed(w)
	if got.Outcome != OutcomeRemoved {
		t.Fatalf("cleanup 1: %+v", got)
	}
	// Restore 1 (recreate): refs passed from the row, applied; the row forgets them.
	run(t, f.project, "worktree", "add", "-q", w.Path, "feat/r1")
	if err := ApplySnapshot(ctx, w.Path, LatestSnapshot(ctx, f.project, "r1", got.SnapshotRefs)); err != nil {
		t.Fatalf("restore 1: %v", err)
	}
	// The user commits that work, then more, and pushes.
	run(t, w.Path, "add", "-A")
	run(t, w.Path, "commit", "-q", "-m", "restored work")
	write(t, filepath.Join(w.Path, "a.txt"), "newer committed content\n")
	write(t, filepath.Join(w.Path, "new.txt"), "added later\n")
	run(t, w.Path, "add", "-A")
	run(t, w.Path, "commit", "-q", "-m", "later work")
	run(t, w.Path, "push", "-q", "origin", "feat/r1")
	// Archive 2: clean and pushed, removed automatically (no new snapshot).
	f.idle()
	w2 := w
	w2.Fence = "200"
	if got := f.one(w2); got.Outcome != OutcomeRemoved {
		t.Fatalf("auto removal 2: %+v", got)
	}
	// Restore 2: the row has no refs any more -> snapshotRefsFor returns nil.
	run(t, f.project, "worktree", "add", "-q", w.Path, "feat/r1")
	ref := LatestSnapshot(ctx, f.project, "r1", nil)
	var err error
	if ref != "" {
		err = ApplySnapshot(ctx, w.Path, ref)
	}
	status := run(t, w.Path, "status", "--porcelain")
	a, _ := os.ReadFile(filepath.Join(w.Path, "a.txt"))
	t.Logf("restore 2 applied %q err=%v; a.txt=%q new.txt exists=%v; status:\n%s", ref, err, a, exists(filepath.Join(w.Path, "new.txt")), status)
	if ref != "" {
		t.Errorf("a repo-wide fallback still finds snapshot %q for a row that has none", ref)
	}
	if status != "" {
		t.Errorf("STALE SNAPSHOT RE-APPLIED: a clean restore of the pushed branch now shows uncommitted changes that revert later commits")
	}
}

// R2: the same, within ONE lifecycle: the branch moved after the clean-up
// (committed to from another workspace) before the restore.
func TestReview3_R2_SnapshotOverAMovedBranch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.worktree("r2", "feat/r2")
	write(t, filepath.Join(w.Path, "u.txt"), "unsaved\n")
	got := f.confirmed(w)
	if got.Outcome != OutcomeRemoved {
		t.Fatalf("cleanup: %+v", got)
	}
	other := filepath.Join(f.root, "proj", "other")
	run(t, f.project, "worktree", "add", "-q", other, "feat/r2")
	write(t, filepath.Join(other, "a.txt"), "moved on\n")
	write(t, filepath.Join(other, "b.txt"), "new file\n")
	run(t, other, "add", "-A")
	run(t, other, "commit", "-q", "-m", "branch moves on")
	run(t, f.project, "worktree", "remove", other)
	run(t, f.project, "worktree", "add", "-q", w.Path, "feat/r2")
	err := ApplySnapshot(ctx, w.Path, LatestSnapshot(ctx, f.project, "r2", got.SnapshotRefs))
	status := run(t, w.Path, "status", "--porcelain")
	t.Logf("apply err=%v status:\n%s", err, status)
	if err == nil {
		t.Errorf("a snapshot was applied although the branch moved on (err=nil)")
	}
	if !strings.Contains(strings.ToLower(errString(err)), "moved") {
		t.Errorf("the refusal must say the branch moved, got %v", err)
	}
	if status != "" {
		t.Errorf("a refused apply must leave the checkout clean, got %q", status)
	}
	if refs, _ := gitOut(f.project, "for-each-ref", "refs/reliant/wip/r2"); refs == "" {
		t.Errorf("a refused apply must KEEP the snapshot refs")
	}
	if err == nil && (strings.Contains(status, "a.txt") || strings.Contains(status, "b.txt")) {
		t.Errorf("snapshot applied over a moved HEAD: the working tree reverts the newer commit (%q)", status)
	}
}

// R3: does ApplySnapshot leave partial writes behind when it fails?
func TestReview3_R3_FailedApplyLeavesPartialState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.worktree("r3", "feat/r3")
	write(t, filepath.Join(w.Path, "a.txt"), "saved\n")
	write(t, filepath.Join(w.Path, "dir", "x.txt"), "x\n")
	got := f.confirmed(w)
	if got.Outcome != OutcomeRemoved {
		t.Fatalf("cleanup: %+v", got)
	}
	run(t, f.project, "worktree", "add", "-q", w.Path, "feat/r3")
	// Make the verify step fail: an ignored-by-nobody file appears mid-apply is
	// hard to time; instead block one write with a read-only directory.
	if err := os.MkdirAll(filepath.Join(w.Path, "dir"), 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(w.Path, "dir"), 0o755)
	err := ApplySnapshot(ctx, w.Path, LatestSnapshot(ctx, f.project, "r3", got.SnapshotRefs))
	a, _ := os.ReadFile(filepath.Join(w.Path, "a.txt"))
	t.Logf("apply err=%v; a.txt now %q (HEAD has %q)", err, a, "one\n")
	if err == nil {
		t.Skip("the read-only directory did not make the apply fail on this filesystem")
	}
	if string(a) != "one\n" {
		t.Errorf("a failed apply left a.txt as %q, want HEAD's %q", a, "one\n")
	}
	if st := run(t, w.Path, "status", "--porcelain"); st != "" {
		t.Errorf("a failed apply must leave the checkout clean, got:\n%s", st)
	}
}

// P9: an empty ORIG_HEAD in the admin dir.
func TestReview3_P9_EmptyPseudoRef(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("p9", "feat/p9")
	admin := run(t, w.Path, "rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(admin, "ORIG_HEAD"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PANIC in classify on an empty ORIG_HEAD: %v", r)
		}
	}()
	got := f.one(w)
	t.Logf("%+v", got)
}

// quarantineDirs lists everything under <root>/.reclaim/<id>.
func quarantineDirs(f *fixture, id string) []string {
	entries, _ := os.ReadDir(filepath.Join(f.root, ".reclaim", id))
	var out []string
	for _, e := range entries {
		out = append(out, filepath.Join(f.root, ".reclaim", id, e.Name()))
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestRecoverQuarantined_PutsACrashedCheckoutBack(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("rq", "feat/rq")
	ctx := context.Background()
	if got := f.r.Reconcile(ctx, Request{Worktrees: []Worktree{w}}).Results[0]; got.Outcome != OutcomeClaimed {
		t.Fatalf("phase one: %+v", got)
	}
	info, _ := inspectLock(ctx, w.Path)
	q := f.r.newQuarantinePath(w, w.Path)
	_ = os.MkdirAll(filepath.Dir(q), 0o755)
	write(t, originMarker(q), w.Path)
	run(t, f.project, "worktree", "unlock", w.Path)
	run(t, f.project, "worktree", "move", w.Path, q)
	_ = setLock(ctx, lockInfo{CommonDir: info.CommonDir}, q, ArchivedLockReason(w.ID, w.Fence))

	restored, parked := f.r.RecoverQuarantined(ctx, "")
	if len(restored) != 1 || len(parked) != 0 || !exists(w.Path) || exists(q) {
		t.Fatalf("restored=%v parked=%v path=%v q=%v", restored, parked, exists(w.Path), exists(q))
	}
	if back, _ := inspectLock(ctx, w.Path); !back.Locked || back.Reason != ArchivedLockReason(w.ID, w.Fence) {
		t.Errorf("lock not preserved: %+v", back)
	}

	// A taken origin stays parked.
	run(t, f.project, "worktree", "unlock", w.Path)
	run(t, f.project, "worktree", "move", w.Path, q)
	write(t, filepath.Join(w.Path, "x.txt"), "x")
	restored, parked = f.r.RecoverQuarantined(ctx, w.ID)
	if len(restored) != 0 || len(parked) != 1 || !exists(q) {
		t.Fatalf("taken origin: restored=%v parked=%v", restored, parked)
	}
}

// F8: two Reclaimers on one root (two processes sharing a $HOME) serialize on the
// row's file lock.
func TestRowLock_SerializesAcrossReclaimersOnOneRoot(t *testing.T) {
	f := newFixture(t)
	other := NewReclaimer(f.r.Root)
	release, err := f.r.rowLock("row1")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		un, err := other.rowLock("row1")
		if err == nil {
			un()
		}
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("a second reclaimer took the row lock while the first held it")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("the second reclaimer never got the lock after release")
	}
	r1, _ := f.r.rowLock("a")
	r2, err := other.rowLock("b")
	if err != nil {
		t.Fatal(err)
	}
	r2()
	r1()
}

// F10: a marker with no checkout beside it is dropped; one with a checkout is kept.
func TestRecoverQuarantined_StrayMarkerOnlyWhenNoCheckout(t *testing.T) {
	f := newFixture(t)
	root := f.r.quarantineRoot("rowx")
	if err := os.MkdirAll(filepath.Join(root, "kept-1-aa"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "kept-1-aa.origin"), "/nowhere")
	write(t, filepath.Join(root, "stray-2-bb.origin"), "/nowhere")
	f.r.RecoverQuarantined(context.Background(), "rowx")
	if exists(filepath.Join(root, "stray-2-bb.origin")) {
		t.Errorf("stray marker should be deleted")
	}
	if !exists(filepath.Join(root, "kept-1-aa.origin")) {
		t.Errorf("a marker with a checkout beside it must be kept")
	}
}
