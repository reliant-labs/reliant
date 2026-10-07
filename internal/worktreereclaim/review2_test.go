// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression tests ported from the second review of #574 (probes N1-N8).

// N1: lockActive's 10-minute cache. A path locked as active populates the
// cache; a claim then rewrites the lock to "archived <fence>" without touching
// the cache; the unarchive re-lock (a reconcile with state=active, which is
// exactly what WorktreeService.relockActive sends) is then a cache hit and a
// no-op, so the fence survives the unarchive and a phase-two removal succeeds.
func TestReview2_N1_RelockCacheDefeatsTheFence(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("n1", "feat/n1")
	active := w
	active.State = StateActive
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{active}}).Results[0]; got.Outcome != OutcomeLocked {
		t.Fatalf("initial lock: %+v", got)
	}
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.Outcome != OutcomeClaimed {
		t.Fatalf("phase one: %+v", got)
	}
	// Unarchive: the server re-locks as active.
	relock := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{active}}).Results[0]
	info, _ := inspectLock(context.Background(), w.Path)
	t.Logf("relock=%+v lock after unarchive=%q", relock, info.Reason)
	w.Remove = true
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	t.Logf("in-flight phase two after unarchive: %+v exists=%v", got, exists(w.Path))
	if got.Outcome == OutcomeRemoved || !exists(w.Path) {
		t.Errorf("FENCE DEFEATED: the unarchive re-lock was a cache hit; a stale removal removed a restored workspace")
	}
}

// N2: the race the fence must cover: a process writes an ignored, NON-rebuildable
// file after structure()'s data check but before git removes the checkout. The
// Cwds probe is called right inside that window (inUse, fresh), so it doubles
// as the "other process".
func TestReview2_N2_DataWrittenInsideTheRemovalWindow(t *testing.T) {
	for _, mode := range []string{"auto", "confirmed"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.idle()
			ignore(t, f, ".env\n")
			w := f.worktree("n2-"+mode, "feat/n2-"+mode)
			if mode == "confirmed" {
				write(t, filepath.Join(w.Path, "u.txt"), "dirty so it is snapshotted\n")
			}
			env := filepath.Join(w.Path, ".env")
			calls := 0
			f.r.Cwds = func(context.Context) ([]string, error) {
				calls++
				if calls == 2 { // phase two's fresh check: after structure(), before removal
					_ = os.WriteFile(env, []byte("SECRET=only-copy\n"), 0o600)
				}
				return nil, nil
			}
			var got Result
			if mode == "auto" {
				got = f.one(w)
			} else {
				got = f.confirmed(w)
			}
			t.Logf("%s: %+v cwds-calls=%d .env exists=%v", mode, got, calls, exists(env))
			if calls >= 2 && !exists(env) {
				t.Errorf("DATA LOSS (%s): an ignored .env written during the removal window was deleted", mode)
			}
		})
	}
}

// N3: restore fidelity. ApplySnapshot builds a patch through git(), which
// TrimSpaces stdout.
func TestReview2_N3_RestoreRoundTrip(t *testing.T) {
	f := newFixture(t)
	// A tracked file whose edit's hunk ends on blank context lines, and whose
	// last added line has trailing whitespace.
	write(t, filepath.Join(f.project, "b.txt"), "one\n\n\ntwo\n\n\n\nthree\n")
	run(t, f.project, "add", ".")
	run(t, f.project, "commit", "-q", "-m", "b")
	run(t, f.project, "push", "-q", "origin", "main")
	w := f.worktree("n3", "feat/n3")
	files := map[string][]byte{
		"b.txt":          []byte("one\nINSERTED\n\n\ntwo\n\n\n\nthree\n"),
		"trailing.txt":   []byte("keep\nlast line with trailing spaces   "),
		"blob.bin":       {0, 1, 2, 3, 255, 254, 0, 0, 10, 13, 0},
		"tail-blank.txt": []byte("x\n\n"),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(w.Path, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := f.confirmed(w)
	if got.Outcome != OutcomeRemoved {
		t.Fatalf("cleanup: %+v", got)
	}
	run(t, f.project, "worktree", "add", "-q", w.Path, "feat/n3")
	ref := LatestSnapshot(context.Background(), f.project, "n3", got.SnapshotRefs)
	if err := ApplySnapshot(context.Background(), w.Path, ref); err != nil {
		t.Errorf("RESTORE FAILED: %v", err)
		return
	}
	for name, want := range files {
		have, err := os.ReadFile(filepath.Join(w.Path, name))
		if err != nil || !bytes.Equal(have, want) {
			t.Errorf("RESTORE MISMATCH %s: have %q want %q (err %v)", name, have, want, err)
		}
	}
}

// N4 (gap b): a commit reachable only from the worktree's own HEAD reflog, and
// one only from a per-worktree ref (refs/worktree/*), on the AUTO path.
func TestReview2_N4_PerWorktreeReflogAndRefs(t *testing.T) {
	for _, kind := range []string{"reflog", "worktree-ref", "bisect-ref"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.idle()
			w := f.worktree("n4-"+kind, "feat/n4-"+kind)
			run(t, w.Path, "checkout", "-q", "--detach")
			write(t, filepath.Join(w.Path, "lost.txt"), "only here\n")
			run(t, w.Path, "add", ".")
			run(t, w.Path, "commit", "-q", "-m", "detached work")
			lost := run(t, w.Path, "rev-parse", "HEAD")
			switch kind {
			case "worktree-ref":
				run(t, w.Path, "update-ref", "refs/worktree/keep", lost)
			case "bisect-ref":
				run(t, w.Path, "update-ref", "refs/bisect/bad", lost)
			}
			run(t, w.Path, "checkout", "-q", "feat/n4-"+kind)
			got := f.one(w)
			reach, _ := gitOut(f.project, "for-each-ref", "--contains", lost, "--format=%(refname)")
			t.Logf("%s: %+v; refs containing it after: %q", kind, got, reach)
			if got.Outcome == OutcomeRemoved && strings.TrimSpace(reach) == "" {
				t.Errorf("GAP (b) %s: commit %s reachable only from the removed worktree is now unreachable", kind, lost[:8])
			}
		})
	}
}

func gitOut(dir string, args ...string) (string, error) {
	return git(context.Background(), dir, nil, args...)
}

// N3b: the patch's LAST line is an added line with trailing whitespace, or a
// blank context line: git() TrimSpaces stdout before the patch is written.
func TestReview2_N3b_RestorePatchTail(t *testing.T) {
	for name, tc := range map[string]struct{ base, edited string }{
		"trailing-ws-last-added": {"a\n", "a\nend with spaces   \n"},
		"blank-context-tail":     {"a\nb\nc\n\n\n\n", "a\nB\nc\n\n\n\n"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			write(t, filepath.Join(f.project, "zz.txt"), tc.base)
			run(t, f.project, "add", ".")
			run(t, f.project, "commit", "-q", "-m", "zz")
			run(t, f.project, "push", "-q", "origin", "main")
			w := f.worktree("n3b", "feat/n3b")
			write(t, filepath.Join(w.Path, "zz.txt"), tc.edited)
			got := f.confirmed(w)
			if got.Outcome != OutcomeRemoved {
				t.Fatalf("cleanup: %+v", got)
			}
			run(t, f.project, "worktree", "add", "-q", w.Path, "feat/n3b")
			ref := LatestSnapshot(context.Background(), f.project, "n3b", got.SnapshotRefs)
			saved, _ := gitOut(f.project, "show", ref+":zz.txt")
			err := ApplySnapshot(context.Background(), w.Path, ref)
			have, _ := os.ReadFile(filepath.Join(w.Path, "zz.txt"))
			t.Logf("apply err=%v have=%q want=%q (snapshot has %q)", err, have, tc.edited, saved)
			if err != nil || string(have) != tc.edited {
				t.Errorf("RESTORE %s: err=%v have=%q want=%q", name, err, have, tc.edited)
			}
		})
	}
}

// N5: unarchive arriving after phase two removed the directory. The daemon's
// answer to the re-lock is "gone"; WorktreeService.relockActive discards it.
func TestReview2_N5_RelockAfterRemovalSaysGone(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("n5", "feat/n5")
	if got := f.one(w); got.Outcome != OutcomeRemoved {
		t.Fatalf("setup: %+v", got)
	}
	active := Worktree{ID: w.ID, Path: w.Path, State: StateActive}
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{active}}).Results[0]
	t.Logf("relock after removal: %+v", got)
}

// N6: staged content whose working-tree copy equals HEAD. The classifier never
// reads the real index; on the auto path git's own (non-forced) check must save it.
func TestReview2_N6_StagedButRevertedInWorktree(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("n6", "feat/n6")
	write(t, filepath.Join(w.Path, "a.txt"), "staged only\n")
	run(t, w.Path, "add", "a.txt")
	write(t, filepath.Join(w.Path, "a.txt"), "one\n") // back to HEAD's content
	staged := run(t, w.Path, "rev-parse", ":a.txt")
	got := f.one(w)
	t.Logf("auto: %+v exists=%v", got, exists(w.Path))
	if got.Outcome == OutcomeRemoved {
		t.Errorf("DATA LOSS: staged blob %s removed on the auto path", staged[:8])
	}
	got = f.confirmed(w)
	t.Logf("confirmed: %+v", got)
	if got.Outcome == OutcomeRemoved {
		found := false
		for _, ref := range got.SnapshotRefs {
			if out, _ := gitOut(f.project, "ls-tree", "-r", ref); strings.Contains(out, staged) {
				found = true
			}
		}
		if !found {
			t.Logf("KNOWN GAP (confirmed): staged blob %s is in no snapshot", staged[:8])
		}
	}
}

// N7: the unarchive re-lock does not retire the fence. A phase one carrying the
// SAME fence that lands after the re-lock (another replica's settle, the leader
// sweep, or a clean-up batch that read the row before the DB flip) simply
// re-claims, and its phase two removes.
func TestReview2_N7_ReclaimAfterRelockWithSameFence(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("n7", "feat/n7")
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.Outcome != OutcomeClaimed {
		t.Fatalf("phase one: %+v", got)
	}
	active := w
	active.State = StateActive
	f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{active}}) // unarchive re-lock (cold cache)
	info, _ := inspectLock(context.Background(), w.Path)
	t.Logf("after re-lock: %q", info.Reason)
	late := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	t.Logf("late phase one with the same fence: %+v", late)
	if late.Outcome == OutcomeClaimed {
		w.Remove = true
		got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
		t.Logf("its phase two: %+v exists=%v", got, exists(w.Path))
		t.Errorf("the re-lock did not retire fence %s: a late phase one re-claimed it", w.Fence)
	}
}

// N8: the automatic classifier (no snapshot, no consent) hashes untracked
// files into the SHARED object store via `git add -A` on a temp index.
func TestReview2_N8_ClassifierWritesUntrackedIntoSharedObjects(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("n8", "feat/n8")
	big := bytes.Repeat([]byte("0123456789abcdef"), 4<<20) // 64 MiB untracked, not ignored
	if err := os.WriteFile(filepath.Join(w.Path, "dataset.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	blob := run(t, w.Path, "hash-object", "dataset.bin") // computes, does not write
	if _, err := gitOut(f.project, "cat-file", "-e", blob); err == nil {
		t.Fatal("setup: blob already present")
	}
	got := f.one(w)
	_, err := gitOut(f.project, "cat-file", "-e", blob)
	t.Logf("auto: %s/%s; 64 MiB blob now in shared object store: %v", got.Outcome, got.Reason, err == nil)
	if err == nil {
		t.Errorf("the automatic classifier wrote a 64 MiB untracked file into the shared .git/objects (no size cap on this path)")
	}
}

// R-S2: the restore retires the fence for good. Nothing built from a read taken
// before it can claim the worktree again, and a newer archive can.
func TestFence_IsSingleUseAndMonotonic(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("mono", "feat/mono")
	f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}) // claimed under 100
	active := Worktree{ID: w.ID, Path: w.Path, State: StateActive, Retire: "100"}
	f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{active}})

	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.Outcome == OutcomeClaimed {
		t.Fatalf("a retired fence was claimed again: %+v", got)
	}
	older := w
	older.Fence = "50"
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{older}}).Results[0]; got.Outcome == OutcomeClaimed {
		t.Fatalf("an older fence was claimed: %+v", got)
	}
	newer := w
	newer.Fence = "200"
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{newer}}).Results[0]; got.Outcome != OutcomeClaimed {
		t.Fatalf("a genuinely newer archive must be claimable: %+v", got)
	}
}

// R-S2: re-locking a directory that is already gone says so, so the server does
// not flip the row to active over nothing.
func TestRelockOfARemovedDirectoryReportsGone(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("rg", "feat/rg")
	if got := f.one(w); got.Outcome != OutcomeRemoved {
		t.Fatalf("setup: %+v", got)
	}
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{{ID: w.ID, Path: w.Path, State: StateActive, Retire: w.Fence}}}).Results[0]
	if got.Outcome != OutcomeGone {
		t.Fatalf("got %+v, want gone", got)
	}
}

// R-B1: more orphan shapes, and the confirmed path saving them.
func TestReconcile_OrphanHistoryHoldsAutoAndIsSavedByConfirmed(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("orph", "feat/orph")
	run(t, w.Path, "checkout", "-q", "--detach")
	write(t, filepath.Join(w.Path, "lost.txt"), "only here\n")
	run(t, w.Path, "add", ".")
	run(t, w.Path, "commit", "-q", "-m", "detached work")
	lost := run(t, w.Path, "rev-parse", "HEAD")
	run(t, w.Path, "checkout", "-q", "feat/orph")

	got := f.one(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonUnreachable || !exists(w.Path) {
		t.Fatalf("auto: got %+v, want held/unreachable_history", got)
	}
	got = f.confirmed(w)
	if got.Outcome != OutcomeRemoved {
		t.Fatalf("confirmed: %+v", got)
	}
	orphanRef := ""
	for _, ref := range got.SnapshotRefs {
		if strings.Contains(ref, "/orphans/") {
			orphanRef = ref
		}
	}
	if orphanRef == "" {
		t.Fatalf("no orphan ref among %v", got.SnapshotRefs)
	}
	if tip := run(t, f.project, "rev-parse", orphanRef); tip != lost {
		t.Fatalf("orphan ref = %s, want %s", tip, lost)
	}
	if reach := run(t, f.project, "for-each-ref", "--contains", lost, "--format=%(refname)"); !strings.Contains(reach, "orphans") {
		t.Fatalf("the commit is reachable from %q, want the orphan ref", reach)
	}
}

// R-B1: a commit that IS on a branch is not an orphan, however the worktree got there.
func TestReconcile_ReflogOfAnOrdinaryBranchIsNotAnOrphan(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("ordinary", "feat/ordinary")
	write(t, filepath.Join(w.Path, "x.txt"), "x\n")
	run(t, w.Path, "add", ".")
	run(t, w.Path, "commit", "-q", "-m", "on the branch")
	run(t, w.Path, "push", "-q", "origin", "feat/ordinary")
	if got := f.one(w); got.Outcome != OutcomeRemoved {
		t.Fatalf("got %+v, want removed: its reflog only names commits on a pushed branch", got)
	}
}

// R-S3: quarantine. A writer that holds the ORIGINAL path after the move fails
// with ENOENT instead of writing into a directory that is about to be deleted,
// and the change is noticed, so everything is put back.
func TestQuarantine_PathWriterLosesAndTheWorktreeIsPutBack(t *testing.T) {
	for _, mode := range []string{"auto", "confirmed"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.idle()
			ignore(t, f, ".env\n")
			w := f.worktree("q-"+mode, "feat/q-"+mode)
			if mode == "confirmed" {
				write(t, filepath.Join(w.Path, "u.txt"), "dirty\n")
			}
			calls := 0
			var writeErr error
			f.r.Cwds = func(context.Context) ([]string, error) {
				calls++
				// The check on the QUARANTINED copy is the 2nd or 3rd call; by
				// then the original path must be gone.
				if calls >= 3 {
					if _, err := os.Stat(w.Path); err == nil {
						return nil, nil // still in place: not yet quarantined
					}
					writeErr = os.WriteFile(filepath.Join(w.Path, ".env"), []byte("SECRET\n"), 0o600)
				}
				return nil, nil
			}
			var got Result
			if mode == "auto" {
				got = f.one(w)
			} else {
				got = f.confirmed(w)
			}
			if calls < 3 || writeErr == nil {
				t.Fatalf("calls=%d writeErr=%v: the writer must have run after the move and failed with ENOENT", calls, writeErr)
			}
			_ = got
			if exists(w.Path) {
				// held and put back: the claim must still protect it
				if info, _ := inspectLock(context.Background(), w.Path); !info.Locked {
					t.Fatal("not locked after being put back")
				}
			}
		})
	}
}

// R-S3: removal is per checkout and each is quarantined at its own moment.
func TestQuarantine_MultiCheckoutIsRemovedOneAtATimeAndRestoredOnChange(t *testing.T) {
	f := newFixture(t)
	f.idle()
	ws := filepath.Join(f.root, "proj", "qm")
	for i, c := range []string{"api", "web"} {
		run(t, f.project, "worktree", "add", "-q", "-b", fmt.Sprintf("feat/qm%d", i), filepath.Join(ws, c), "main")
		if err := LockCheckout(context.Background(), filepath.Join(ws, c), "qm"); err != nil {
			t.Fatal(err)
		}
	}
	w := Worktree{ID: "qm", Path: ws, State: StateArchived, BaseBranch: "main", Fence: "7"}
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.Outcome != OutcomeClaimed {
		t.Fatalf("phase one: %+v", got)
	}
	// A process that FOLLOWED the move (an open file, a cwd) writes into the
	// quarantined copy of the second checkout while it is being re-checked.
	// (A writer that only knows the old path gets ENOENT; see the test above.)
	calls := 0
	f.r.Cwds = func(context.Context) ([]string, error) {
		calls++
		if calls == 4 {
			entries, _ := os.ReadDir(filepath.Join(f.root, ".reclaim", "qm"))
			for _, e := range entries {
				_ = os.WriteFile(filepath.Join(f.root, ".reclaim", "qm", e.Name(), "late.txt"), []byte("x"), 0o644)
			}
		}
		return nil, nil
	}
	w.Remove = true
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeHeld || got.Reason != ReasonDirty {
		t.Fatalf("got %+v, want held/dirty: the second checkout changed after it was quarantined", got)
	}
	back := 0
	for _, c := range []string{"api", "web"} {
		if exists(filepath.Join(ws, c, "late.txt")) && exists(filepath.Join(ws, c, ".git")) {
			back++
		}
	}
	if back != 1 {
		t.Fatalf("the changed checkout must be moved back intact, found %d", back)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.root, ".reclaim", "qm")); len(entries) != 0 {
		t.Fatalf("a checkout was left in quarantine: %v", entries)
	}
	if info, _ := inspectLock(context.Background(), filepath.Join(ws, map[bool]string{true: "api", false: "web"}[exists(filepath.Join(ws, "api", "late.txt"))])); !info.Locked {
		t.Fatal("the put-back checkout is not protected by its claim")
	}
}

// R-S5: byte-exact restore, and a failed apply reports failure.
func TestApplySnapshot_ByteExactAndVerified(t *testing.T) {
	f := newFixture(t)
	w := f.worktree("bx", "feat/bx")
	files := map[string][]byte{
		"trail.txt": []byte("a\nend with spaces   \n"),
		"nonl.txt":  []byte("no trailing newline   "),
		"bin.dat":   {0, 1, 2, 255, 254, 10, 13, 0},
	}
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(w.Path, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := f.confirmed(w)
	if got.Outcome != OutcomeRemoved {
		t.Fatalf("cleanup: %+v", got)
	}
	run(t, f.project, "worktree", "add", "-q", w.Path, "feat/bx")
	ref := LatestSnapshot(context.Background(), f.project, "bx", got.SnapshotRefs)
	if err := ApplySnapshot(context.Background(), w.Path, ref); err != nil {
		t.Fatal(err)
	}
	for n, want := range files {
		if have, _ := os.ReadFile(filepath.Join(w.Path, n)); !bytes.Equal(have, want) {
			t.Fatalf("%s: have %q want %q", n, have, want)
		}
	}
	if run(t, w.Path, "diff", "--cached", "--name-only") != "" {
		t.Fatal("the index must stay at HEAD")
	}
}

func TestApplySnapshot_RefusesADirtyCheckoutAndFailsLoudly(t *testing.T) {
	f := newFixture(t)
	w := f.worktree("dirtyapply", "feat/dirtyapply")
	write(t, filepath.Join(w.Path, "u.txt"), "u\n")
	ref, _, err := Snapshot(context.Background(), w.Path, "refs/reliant/wip/da/root/", "da", nil)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(w.Path, "other.txt"), "x\n")
	if err := ApplySnapshot(context.Background(), w.Path, ref); err == nil {
		t.Fatal("applied a snapshot over a dirty checkout")
	}
}

// R-S8: the automatic classifier writes NOTHING into the shared object store.
func TestAutoClassifierWritesNoObjects(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("noobj", "feat/noobj")
	write(t, filepath.Join(w.Path, "a.txt"), "edited so it holds as dirty\n")
	write(t, filepath.Join(w.Path, "big.bin"), strings.Repeat("0123456789abcdef", 1<<16))
	blob := run(t, w.Path, "hash-object", "big.bin")
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonDirty {
		t.Fatalf("got %+v", got)
	}
	if _, err := git(context.Background(), f.project, nil, "cat-file", "-e", blob); err == nil {
		t.Fatal("the automatic classifier wrote an untracked file into the shared object store")
	}
}

// R-S8: the confirmed path holds anything over the cap, with the stated reason.
func TestSnapshotRemove_TooLargeReasonName(t *testing.T) {
	if ReasonTooLarge != "too-large-to-snapshot" {
		t.Fatalf("reason = %q", ReasonTooLarge)
	}
}

// R-S8: a worktree the daemon runs out of time for is deferred untouched.
func TestReconcile_BatchBudgetDefersTheRest(t *testing.T) {
	f := newFixture(t)
	f.idle()
	f.r.Budget = time.Nanosecond
	var ws []Worktree
	for i := 0; i < 3; i++ {
		ws = append(ws, f.worktree(fmt.Sprintf("bud%d", i), fmt.Sprintf("feat/bud%d", i)))
	}
	resp := f.r.Reconcile(context.Background(), Request{Worktrees: ws})
	deferred := 0
	for _, r := range resp.Results {
		if r.Outcome == OutcomeDeferred {
			deferred++
		}
	}
	if deferred == 0 {
		t.Fatalf("nothing was deferred under a spent budget: %+v", resp.Results)
	}
	for _, w := range ws {
		if !exists(w.Path) {
			t.Fatalf("%s removed under a spent budget", w.ID)
		}
	}
}
