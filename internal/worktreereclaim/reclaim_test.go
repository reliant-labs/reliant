// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture is a project repo with an "origin" it can push to, and a root under
// which worktrees live.
type fixture struct {
	t       *testing.T
	project string
	origin  string
	root    string
	r       *Reclaimer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	base := t.TempDir()
	f := &fixture{t: t, project: filepath.Join(base, "project"), origin: filepath.Join(base, "origin.git"), root: filepath.Join(base, "worktrees")}
	run(t, base, "init", "-q", "--bare", "-b", "main", f.origin)
	run(t, base, "init", "-q", "-b", "main", f.project)
	write(t, filepath.Join(f.project, "a.txt"), "one\n")
	run(t, f.project, "add", ".")
	run(t, f.project, "commit", "-q", "-m", "init")
	run(t, f.project, "remote", "add", "origin", f.origin)
	run(t, f.project, "push", "-q", "origin", "main")
	if err := os.MkdirAll(f.root, 0o755); err != nil {
		t.Fatal(err)
	}
	f.r = &Reclaimer{Root: f.root, Cwds: func(context.Context) ([]string, error) { return nil, nil }}
	return f
}

// worktree creates a linked worktree the way the daemon does and locks it.
func (f *fixture) worktree(id, branch string) Worktree {
	f.t.Helper()
	path := filepath.Join(f.root, "proj", id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	run(f.t, f.project, "worktree", "add", "-q", "-b", branch, path, "main")
	if err := LockCheckout(context.Background(), path, id); err != nil {
		f.t.Fatal(err)
	}
	return Worktree{ID: id, Path: path, State: StateArchived, BaseBranch: "main", Fence: "100"}
}

// one plays the server for an automatic pass: phase one decides and claims;
// when it claims, phase two (sent after the server re-reads the row) removes.
func (f *fixture) one(w Worktree) Result {
	f.t.Helper()
	if w.Fence == "" && w.State == StateArchived {
		w.Fence = "100"
	}
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeClaimed {
		return got
	}
	w.Remove = true
	return f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
}

// confirmed plays the server for a user-confirmed clean-up (both phases).
func (f *fixture) confirmed(w Worktree) Result {
	f.t.Helper()
	if w.Fence == "" {
		w.Fence = "100"
	}
	got := f.r.SnapshotRemove(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeClaimed {
		return got
	}
	w.Remove, w.Trees = true, got.Trees
	out := f.r.SnapshotRemove(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	out.SnapshotRefs = append(got.SnapshotRefs, out.SnapshotRefs...)
	return out
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// idle makes the fixture's worktrees look untouched for a day, so the idle
// floor does not interfere with tests about other rules.
func (f *fixture) idle() {
	f.r.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	f.r.IdleFloor = time.Hour
}

func ignore(t *testing.T, f *fixture, patterns string) {
	t.Helper()
	write(t, filepath.Join(f.project, ".gitignore"), patterns)
	run(t, f.project, "add", ".gitignore")
	run(t, f.project, "commit", "-q", "-m", "ignore")
	run(t, f.project, "push", "-q", "origin", "main")
}

func TestReconcile_CleanMergedIsRemoved(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("w1", "feat/a")
	got := f.one(w)
	if got.Outcome != OutcomeRemoved {
		t.Fatalf("got %+v, want removed", got)
	}
	if exists(w.Path) {
		t.Fatal("directory still exists")
	}
	if out := run(t, f.project, "worktree", "list", "--porcelain"); strings.Contains(out, w.Path) {
		t.Fatalf("registration not removed:\n%s", out)
	}
	run(t, f.project, "rev-parse", "--verify", "refs/heads/feat/a") // branch is never deleted
}

// B2: ignored files are potentially work. Rebuildable output goes; data holds.
func TestReconcile_RebuildableIgnoredFilesDoNotBlock(t *testing.T) {
	f := newFixture(t)
	f.idle()
	ignore(t, f, "node_modules/\ndist/\n")
	w := f.worktree("w2", "feat/b")
	write(t, filepath.Join(w.Path, "node_modules", "x", "big.js"), "junk")
	write(t, filepath.Join(w.Path, "dist", "app.js"), "built")
	if got := f.one(w); got.Outcome != OutcomeRemoved {
		t.Fatalf("got %+v, want removed", got)
	}
}

func TestReconcile_IgnoredDataHoldsTheWorktree(t *testing.T) {
	f := newFixture(t)
	f.idle()
	ignore(t, f, "data/\n.env*\n*.db\n*.sqlite\nsecrets/\n.forge/\nweird/\n")
	for name, rel := range map[string]string{
		"data": "data/logs/l.log", "env": ".env.local", "db": "local.db", "sqlite": "x/app.sqlite",
		"secrets": "secrets/key", "hostinfra": ".forge/hostinfra/pg/PG_VERSION", "unknown": "weird/thing.bin",
	} {
		w := f.worktree("d-"+name, "feat/d-"+name)
		write(t, filepath.Join(w.Path, rel), "x")
		got := f.one(w)
		if got.Outcome != OutcomeHeld || got.Reason != ReasonData {
			t.Fatalf("%s: got %+v, want held/data", name, got)
		}
		if !exists(filepath.Join(w.Path, rel)) {
			t.Fatalf("%s: data was deleted", name)
		}
		// The confirmed clean-up must not remove it either.
		if got := f.confirmed(w); got.Outcome != OutcomeHeld || got.Reason != ReasonData || !exists(filepath.Join(w.Path, rel)) {
			t.Fatalf("%s confirmed: got %+v, want held/data", name, got)
		}
	}
}

func TestReconcile_DirtyIsHeld(t *testing.T) {
	f := newFixture(t)
	f.idle()
	for name, mutate := range map[string]func(Worktree){
		"modified":  func(w Worktree) { write(t, filepath.Join(w.Path, "a.txt"), "changed\n") },
		"untracked": func(w Worktree) { write(t, filepath.Join(w.Path, "new.txt"), "x") },
	} {
		w := f.worktree("dirty-"+name, "feat/dirty-"+name)
		mutate(w)
		got := f.one(w)
		if got.Outcome != OutcomeHeld || got.Reason != ReasonDirty {
			t.Fatalf("%s: got %+v, want held/dirty", name, got)
		}
		if !exists(w.Path) || got.SizeBytes <= 0 {
			t.Fatalf("%s: removed or unsized: %+v", name, got)
		}
	}
}

// B3
func TestReconcile_SkipWorktreeAndAssumeUnchangedEditsAreDirty(t *testing.T) {
	f := newFixture(t)
	f.idle()
	for _, flag := range []string{"--skip-worktree", "--assume-unchanged"} {
		id := "sw" + strings.TrimLeft(flag, "-")
		w := f.worktree(id, "feat/"+id)
		run(t, w.Path, "update-index", flag, "a.txt")
		write(t, filepath.Join(w.Path, "a.txt"), "local override that exists nowhere else\n")
		if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonDirty || !exists(w.Path) {
			t.Fatalf("%s: got %+v, want held/dirty", flag, got)
		}
		if got := f.confirmed(w); got.Outcome == OutcomeRemoved && !snapshotHas(t, f, got, "a.txt", "local override") {
			t.Fatalf("%s: removed without saving the edit", flag)
		}
	}
}

func snapshotHas(t *testing.T, f *fixture, r Result, file, content string) bool {
	t.Helper()
	for _, ref := range r.SnapshotRefs {
		if out, err := exec.Command("git", "-C", f.project, "show", ref+":"+file).Output(); err == nil && strings.Contains(string(out), content) {
			return true
		}
	}
	return false
}

func TestReconcile_UnpushedCommitIsHeld_ThenSafeOnceMergedOrPushed(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("w3", "feat/c")
	write(t, filepath.Join(w.Path, "b.txt"), "b\n")
	run(t, w.Path, "add", ".")
	run(t, w.Path, "commit", "-q", "-m", "work")
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnpushed {
		t.Fatalf("got %+v, want held/unpushed", got)
	}
	run(t, w.Path, "push", "-q", "origin", "feat/c")
	if got := f.one(w); got.Outcome != OutcomeRemoved {
		t.Fatalf("after push: got %+v, want removed", got)
	}
}

func TestReconcile_InUseIsHeld_AndRecheckedRightBeforeRemoval(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("w4", "feat/d")
	f.r.Cwds = func(context.Context) ([]string, error) { return []string{filepath.Join(w.Path, "sub")}, nil }
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonInUse {
		t.Fatalf("got %+v, want held/in_use", got)
	}

	// S3: a process that appears between the decision and the removal.
	f.r.Cwds = func(context.Context) ([]string, error) { return nil, nil }
	w2 := f.worktree("w4b", "feat/d2")
	first := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w2}}).Results[0]
	if first.Outcome != OutcomeClaimed {
		t.Fatalf("phase one: %+v", first)
	}
	f.r.Cwds = func(context.Context) ([]string, error) { return []string{w2.Path}, nil }
	w2.Remove = true
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w2}}).Results[0]; got.Outcome != OutcomeHeld || got.Reason != ReasonInUse || !exists(w2.Path) {
		t.Fatalf("phase two: got %+v, want held/in_use with the directory intact", got)
	}
}

func TestReconcile_CwdProbeFailureFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("w5", "feat/e")
	f.r.Cwds = func(context.Context) ([]string, error) { return nil, os.ErrPermission }
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnverified || !exists(w.Path) {
		t.Fatalf("got %+v, want held/unverified", got)
	}
}

// S3: the automatic path waits out an idle floor; the confirmed one does not.
func TestReconcile_IdleFloorAppliesToAutoOnly(t *testing.T) {
	f := newFixture(t)
	f.r.IdleFloor = time.Hour
	w := f.worktree("w6", "feat/f")
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonRecentlyActive || !exists(w.Path) {
		t.Fatalf("auto: got %+v, want held/recently_active", got)
	}
	write(t, filepath.Join(w.Path, "x.txt"), "x")
	if got := f.confirmed(w); got.Outcome != OutcomeRemoved {
		t.Fatalf("confirmed: got %+v, want removed despite recent activity", got)
	}
}

func TestReconcile_ForeignLockIsUnmanaged(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("w7", "feat/g")
	run(t, f.project, "worktree", "unlock", w.Path)
	run(t, f.project, "worktree", "lock", "--reason", "somebody else", w.Path)
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnmanaged {
		t.Fatalf("got %+v, want held/unmanaged", got)
	}
}

// S2: a directory locked for ANOTHER row's id is not this row's to remove.
func TestReconcile_DirectoryLockedForAnotherRowIsNeverRemoved(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("active-row", "feat/shared")
	w.ID = "archived-row"
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnmanaged || !exists(w.Path) {
		t.Fatalf("got %+v, want held/unmanaged", got)
	}
	if got := f.confirmed(w); got.Outcome == OutcomeRemoved || !exists(w.Path) {
		t.Fatalf("confirmed removed another row's directory: %+v", got)
	}
}

// S1: the fence. An unarchive re-locks as active; a request already in flight
// must then refuse to remove.
func TestFence_UnarchiveInvalidatesInFlightRemoval(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("fence1", "feat/fence1")
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.Outcome != OutcomeClaimed {
		t.Fatalf("phase one: %+v", got)
	}
	// The user restores the workspace: the server re-locks it as active.
	active := w
	active.State, active.Retire = StateActive, w.Fence
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{active}}).Results[0]; got.Outcome != OutcomeLocked {
		t.Fatalf("restore: %+v", got)
	}
	w.Remove = true
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome == OutcomeRemoved || !exists(w.Path) {
		t.Fatalf("removed a restored workspace: %+v", got)
	}
	info, err := inspectLock(context.Background(), w.Path)
	if err != nil || info.Reason != RestoredLockReason("fence1", w.Fence) {
		t.Fatalf("lock = %+v, %v; want the restored lock that remembers the retired fence", info, err)
	}
}

func TestFence_ReArchiveWithNewFenceSupersedesTheOld(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("fence2", "feat/fence2")
	f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}})
	w.Fence = "200"
	f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}})
	stale := w
	stale.Fence, stale.Remove = "100", true
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{stale}}).Results[0]; got.Outcome == OutcomeRemoved || !exists(w.Path) {
		t.Fatalf("an old fence removed the worktree: %+v", got)
	}
}

func TestReconcile_PathOutsideRootIsForeign(t *testing.T) {
	f := newFixture(t)
	got := f.one(Worktree{ID: "x", Path: f.project, State: StateArchived})
	if got.Outcome != OutcomeForeign || !exists(filepath.Join(f.project, ".git")) {
		t.Fatalf("got %+v, want foreign and the project untouched", got)
	}
}

func TestReconcile_MainCheckoutUnderRootIsRefused(t *testing.T) {
	f := newFixture(t)
	f.idle()
	inside := filepath.Join(f.root, "clone")
	run(t, f.root, "clone", "-q", f.origin, inside)
	got := f.one(Worktree{ID: "m", Path: inside, State: StateArchived, BaseBranch: "main"})
	if got.Outcome != OutcomeHeld || got.Reason != ReasonUnmanaged || !exists(inside) {
		t.Fatalf("got %+v, want held/unmanaged", got)
	}
}

// S10: a missing root is an unmounted volume, not evidence of absence.
func TestReconcile_MissingRootIsForeignNotGone(t *testing.T) {
	f := newFixture(t)
	r := &Reclaimer{Root: filepath.Join(t.TempDir(), "not-mounted"), Cwds: f.r.Cwds}
	got := r.Reconcile(context.Background(), Request{Worktrees: []Worktree{{ID: "x", Path: filepath.Join(r.Root, "proj", "x"), State: StateArchived, Fence: "1"}}}).Results[0]
	if got.Outcome != OutcomeForeign {
		t.Fatalf("got %+v, want foreign", got)
	}
}

func TestReconcile_MissingDirectoryPrunesRegistrationAndReportsGone(t *testing.T) {
	f := newFixture(t)
	w := f.worktree("w8", "feat/h")
	run(t, f.project, "worktree", "unlock", w.Path)
	if err := os.RemoveAll(w.Path); err != nil {
		t.Fatal(err)
	}
	w.Checkouts = []Checkout{{RepoPath: f.project}}
	if got := f.one(w); got.Outcome != OutcomeGone {
		t.Fatalf("got %+v, want gone", got)
	}
	if out := run(t, f.project, "worktree", "list", "--porcelain"); strings.Contains(out, "w8") {
		t.Fatalf("stale registration not pruned:\n%s", out)
	}
}

// S10 + S5: when the directory is gone, saved work from an earlier clean-up is
// reported so the server never loses its refs.
func TestReconcile_GoneReportsEarlierSnapshotRefs(t *testing.T) {
	f := newFixture(t)
	run(t, f.project, "update-ref", "refs/reliant/wip/w9/root/1-aa", run(t, f.project, "rev-parse", "HEAD"))
	w := Worktree{ID: "w9", Path: filepath.Join(f.root, "proj", "w9"), State: StateArchived, Fence: "1", Checkouts: []Checkout{{RepoPath: f.project}}}
	got := f.one(w)
	if got.Outcome != OutcomeGone || len(got.SnapshotRefs) != 1 || got.SnapshotRefs[0] != "refs/reliant/wip/w9/root/1-aa" {
		t.Fatalf("got %+v, want gone carrying the existing ref", got)
	}
}

func TestReconcile_ActiveWorktreesAreLockedAndNeverRemoved(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "proj", "w10")
	run(t, f.project, "worktree", "add", "-q", "-b", "feat/i", path, "main")
	got := f.one(Worktree{ID: "w10", Path: path, State: StateActive})
	if got.Outcome != OutcomeLocked || !exists(path) {
		t.Fatalf("got %+v, want locked", got)
	}
	info, err := inspectLock(context.Background(), path)
	if err != nil || !info.Locked || info.Reason != LockReason("w10") {
		t.Fatalf("lock = %+v, %v", info, err)
	}
}

func TestReconcile_ActiveOnlyPassNeverListsProcesses(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "proj", "w11")
	run(t, f.project, "worktree", "add", "-q", "-b", "feat/j", path, "main")
	f.r.Cwds = func(context.Context) ([]string, error) {
		t.Fatal("process list requested for an active-only pass")
		return nil, nil
	}
	if got := f.one(Worktree{ID: "w11", Path: path, State: StateActive}); got.Outcome != OutcomeLocked {
		t.Fatalf("got %+v", got)
	}
}

// R-S1: there is no re-lock cache. A worktree that someone unlocked is locked
// again by the very next request.
func TestLockActive_AlwaysRewritesTheLock(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "proj", "w12")
	run(t, f.project, "worktree", "add", "-q", "-b", "feat/k", path, "main")
	w := Worktree{ID: "w12", Path: path, State: StateActive}
	f.one(w)
	run(t, f.project, "worktree", "unlock", path)
	f.one(w)
	if info, _ := inspectLock(context.Background(), path); !info.Locked {
		t.Fatal("not re-locked by the next request")
	}
}

// B1: files at a multi-repo workspace root are work.
func TestReconcile_MultiRepoRootFilesHoldTheWorkspace(t *testing.T) {
	f := newFixture(t)
	f.idle()
	ws := filepath.Join(f.root, "proj", "taxes-237184bf")
	for i, c := range []string{"api", "web"} {
		run(t, f.project, "worktree", "add", "-q", "-b", fmt.Sprintf("feat/m%d", i), filepath.Join(ws, c), "main")
		if err := LockCheckout(context.Background(), filepath.Join(ws, c), "taxes"); err != nil {
			t.Fatal(err)
		}
	}
	notes := filepath.Join(ws, "NOTES.md")
	write(t, notes, "irreplaceable notes written at the workspace root\n")
	write(t, filepath.Join(ws, "scratch", "plan.txt"), "a plan\n")
	w := Worktree{ID: "taxes", Path: ws, State: StateArchived, BaseBranch: "main", Fence: "5"}
	got := f.one(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonOutsideFiles || !exists(notes) {
		t.Fatalf("auto: got %+v, want held/files-outside-checkout with NOTES.md intact", got)
	}
	got = f.confirmed(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonOutsideFiles || !exists(notes) || !exists(filepath.Join(ws, "api")) {
		t.Fatalf("confirmed: got %+v, want held/files-outside-checkout, nothing removed", got)
	}
}

func TestReconcile_MultiRepoWorkspaceWithOnlyEmptyDirsBetweenIsRemoved(t *testing.T) {
	f := newFixture(t)
	f.idle()
	ws := filepath.Join(f.root, "proj", "ws1")
	for i, c := range []string{"api", "pkg/web"} {
		run(t, f.project, "worktree", "add", "-q", "-b", fmt.Sprintf("feat/e%d", i), filepath.Join(ws, c), "main")
		if err := LockCheckout(context.Background(), filepath.Join(ws, c), "ws1"); err != nil {
			t.Fatal(err)
		}
	}
	w := Worktree{ID: "ws1", Path: ws, State: StateArchived, BaseBranch: "main", Fence: "5"}
	write(t, filepath.Join(ws, "web-dirty.txt"), "x") // outside: holds
	if got := f.one(w); got.Outcome != OutcomeHeld {
		t.Fatalf("got %+v", got)
	}
	os.Remove(filepath.Join(ws, "web-dirty.txt"))
	write(t, filepath.Join(ws, "pkg", "web", "dirty.txt"), "x") // inside a checkout: dirty
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonDirty || !strings.Contains(got.Detail, "pkg/web") {
		t.Fatalf("got %+v, want held/dirty naming pkg/web", got)
	}
	os.Remove(filepath.Join(ws, "pkg", "web", "dirty.txt"))
	if got := f.one(w); got.Outcome != OutcomeRemoved || exists(ws) {
		t.Fatalf("got %+v exists=%v, want whole workspace removed", got, exists(ws))
	}
}

// B4: a repository inside the worktree, at any depth, in ignored or skip dirs.
func TestReconcile_NestedRepositoryHoldsAtAnyDepth(t *testing.T) {
	f := newFixture(t)
	f.idle()
	ignore(t, f, "tmp/\nnode_modules/\n")
	for name, rel := range map[string]string{
		"deep-ignored": "tmp/a/b/c/d/repo",
		"node-modules": "node_modules/pkg/repo",
		"vendor":       "vendor/lib",
		"shallow":      "sub",
	} {
		w := f.worktree("nest-"+name, "feat/nest-"+name)
		nested := filepath.Join(w.Path, filepath.FromSlash(rel))
		write(t, filepath.Join(nested, "work.txt"), "only copy\n")
		run(t, nested, "init", "-q", "-b", "main")
		run(t, nested, "add", ".")
		run(t, nested, "commit", "-q", "-m", "only copy of this commit")
		got := f.one(w)
		if got.Outcome != OutcomeHeld || got.Reason != ReasonNestedRepo || !exists(nested) {
			t.Fatalf("%s auto: got %+v, want held/nested-repository", name, got)
		}
		got = f.confirmed(w)
		if got.Outcome != OutcomeHeld || got.Reason != ReasonNestedRepo || !exists(nested) {
			t.Fatalf("%s confirmed: got %+v, want held/nested-repository", name, got)
		}
		if len(got.SnapshotRefs) != 0 {
			t.Fatalf("%s: a snapshot was taken of a worktree holding a nested repo", name)
		}
	}
}

// B4: the snapshot itself refuses to record a gitlink as a stand-in.
func TestSnapshot_RefusesAGitlinkStandIn(t *testing.T) {
	f := newFixture(t)
	w := f.worktree("gl", "feat/gl")
	nested := filepath.Join(w.Path, "lib")
	write(t, filepath.Join(nested, "x.txt"), "x")
	run(t, nested, "init", "-q", "-b", "main")
	run(t, nested, "add", ".")
	run(t, nested, "commit", "-q", "-m", "n")
	if _, _, err := Snapshot(context.Background(), w.Path, "refs/reliant/wip/gl/root/", "gl", nil); err == nil {
		t.Fatal("snapshot recorded a nested repository as a bare gitlink")
	}
}

func TestSubmoduleIsHeldNotRemoved(t *testing.T) {
	f := newFixture(t)
	f.idle()
	base := filepath.Dir(f.project)
	sub := filepath.Join(base, "sub")
	run(t, base, "init", "-q", "-b", "main", sub)
	write(t, filepath.Join(sub, "s.txt"), "s\n")
	run(t, sub, "add", ".")
	run(t, sub, "commit", "-q", "-m", "s")
	run(t, f.project, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "libs/sub")
	run(t, f.project, "commit", "-q", "-m", "add sub")
	run(t, f.project, "push", "-q", "origin", "main")
	w := f.worktree("sm1", "feat/sm1")
	run(t, w.Path, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")
	if got := f.one(w); got.Outcome != OutcomeHeld || !exists(filepath.Join(w.Path, "libs", "sub")) {
		t.Fatalf("got %+v, want held with the submodule intact", got)
	}
}

func TestSnapshotRemove_SavesWorkThenRemoves(t *testing.T) {
	f := newFixture(t)
	ignore(t, f, "node_modules/\n")
	w := f.worktree("w13", "feat/l")
	write(t, filepath.Join(w.Path, "a.txt"), "edited\n")
	write(t, filepath.Join(w.Path, "untracked.txt"), "keep me\n")
	write(t, filepath.Join(w.Path, "node_modules", "junk"), "junk")
	run(t, w.Path, "add", "a.txt")
	headBefore := run(t, w.Path, "rev-parse", "HEAD")

	got := f.confirmed(w)
	if got.Outcome != OutcomeRemoved || len(got.SnapshotRefs) != 1 {
		t.Fatalf("got %+v, want removed with one snapshot ref", got)
	}
	ref := got.SnapshotRefs[0]
	if !strings.HasPrefix(ref, "refs/reliant/wip/w13/root/") {
		t.Fatalf("ref = %q", ref)
	}
	if exists(w.Path) {
		t.Fatal("directory still exists")
	}
	if parent := run(t, f.project, "rev-parse", ref+"^"); parent != headBefore {
		t.Fatalf("snapshot parent = %s, want HEAD %s", parent, headBefore)
	}
	if got := run(t, f.project, "show", ref+":a.txt"); got != "edited" {
		t.Fatalf("a.txt = %q", got)
	}
	if got := run(t, f.project, "show", ref+":untracked.txt"); got != "keep me" {
		t.Fatalf("untracked.txt = %q", got)
	}
	if out := run(t, f.project, "ls-tree", "-r", "--name-only", ref); strings.Contains(out, "node_modules") {
		t.Fatalf("rebuildable output leaked into the snapshot:\n%s", out)
	}
	if out := run(t, f.origin, "for-each-ref"); strings.Contains(out, "wip") {
		t.Fatalf("snapshot was pushed:\n%s", out)
	}
}

func TestSnapshot_DoesNotTouchHeadIndexOrFiles(t *testing.T) {
	f := newFixture(t)
	w := f.worktree("w14", "feat/m")
	write(t, filepath.Join(w.Path, "a.txt"), "edited\n")
	write(t, filepath.Join(w.Path, "u.txt"), "u\n")
	run(t, w.Path, "add", "a.txt")
	head := run(t, w.Path, "rev-parse", "HEAD")
	statusBefore := run(t, w.Path, "status", "--porcelain")
	if _, _, err := Snapshot(context.Background(), w.Path, "refs/reliant/wip/w14/root/", "w14", nil); err != nil {
		t.Fatal(err)
	}
	if got := run(t, w.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved")
	}
	if got := run(t, w.Path, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("status changed:\n%q\n%q", statusBefore, got)
	}
}

// S11: refs are unique and never overwritten, even within one nanosecond tick.
func TestSnapshot_RefsAreUniqueAndNeverOverwritten(t *testing.T) {
	f := newFixture(t)
	w := f.worktree("w15", "feat/n")
	write(t, filepath.Join(w.Path, "u.txt"), "u\n")
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		ref, _, err := Snapshot(context.Background(), w.Path, "refs/reliant/wip/w15/root/", "w15", nil)
		if err != nil {
			t.Fatal(err)
		}
		if seen[ref] {
			t.Fatalf("ref %s reused", ref)
		}
		seen[ref] = true
	}
	head := run(t, f.project, "rev-parse", "HEAD")
	if _, err := createRef(context.Background(), f.project, "refs/reliant/wip/x/", head); err != nil {
		t.Fatal(err)
	}
	// An existing ref name must make update-ref fail rather than overwrite.
	ref := run(t, f.project, "for-each-ref", "--format=%(refname)", "refs/reliant/wip/x")
	other := run(t, f.project, "rev-parse", "HEAD~0")
	if _, err := git(context.Background(), f.project, nil, "update-ref", ref, other, ""); err == nil {
		t.Fatal("update-ref with an empty old value overwrote an existing ref")
	}
}

// S3: the work changes between the snapshot and the removal.
func TestSnapshotRemove_WorkChangedAfterSnapshotIsNotRemoved(t *testing.T) {
	f := newFixture(t)
	w := f.worktree("w16", "feat/o")
	write(t, filepath.Join(w.Path, "u.txt"), "v1\n")
	w.Fence = "100"
	p1 := f.r.SnapshotRemove(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if p1.Outcome != OutcomeClaimed {
		t.Fatalf("phase one: %+v", p1)
	}
	write(t, filepath.Join(w.Path, "late.txt"), "written after the snapshot\n")
	w.Remove, w.Trees = true, p1.Trees
	got := f.r.SnapshotRemove(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome == OutcomeRemoved || !exists(filepath.Join(w.Path, "late.txt")) {
		t.Fatalf("removed work that changed after it was saved: %+v", got)
	}
}

// S3: no --force. git itself refuses a dirty checkout at removal time.
func TestRemoval_NeverForcesPastGit(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("w17", "feat/p")
	p1 := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if p1.Outcome != OutcomeClaimed {
		t.Fatalf("phase one: %+v", p1)
	}
	checkouts := []string{w.Path}
	write(t, filepath.Join(w.Path, "raced.txt"), "x") // lands after classification
	got := f.r.removeWorkspace(context.Background(), Worktree{ID: w.ID, Path: w.Path, Fence: w.Fence}, checkouts, false)
	if got.Outcome == OutcomeRemoved || !exists(filepath.Join(w.Path, "raced.txt")) {
		t.Fatalf("got %+v, want git to refuse a dirty checkout", got)
	}
	if info, _ := inspectLock(context.Background(), w.Path); !info.Locked {
		t.Fatal("claim was not restored after git refused")
	}
}

func TestSnapshotRemove_InUseIsLeftAlone(t *testing.T) {
	f := newFixture(t)
	w := f.worktree("w18", "feat/q")
	write(t, filepath.Join(w.Path, "u.txt"), "u\n")
	f.r.Cwds = func(context.Context) ([]string, error) { return []string{w.Path}, nil }
	got := f.confirmed(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonInUse || !exists(w.Path) {
		t.Fatalf("got %+v, want held/in_use and directory intact", got)
	}
	if out := run(t, f.project, "for-each-ref", "refs/reliant"); out != "" {
		t.Fatalf("snapshot taken for an in-use worktree: %s", out)
	}
}

func TestSnapshotRemove_TooLargeIsHeld(t *testing.T) {
	f := newFixture(t)
	w := f.worktree("w19", "feat/r")
	big := make([]byte, 1<<20)
	for i := 0; i < 520; i++ {
		write(t, filepath.Join(w.Path, "big", fmt.Sprintf("%d.bin", i)), string(big[:1<<20-1])+fmt.Sprint(i))
	}
	got := f.confirmed(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonTooLarge || !exists(w.Path) {
		t.Fatalf("got %+v, want held/too_large", got)
	}
}

func TestReconcile_KeepFilesNeverRemoves(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("w20", "feat/s")
	w.KeepFiles = true
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonKept || !exists(w.Path) {
		t.Fatalf("got %+v, want held/kept (visible in the inbox)", got)
	}
	if !ReasonKept.Removable() {
		t.Fatal("kept worktrees must be removable by the confirmed Clean up")
	}
	// Held things are still reported while keeping.
	d := f.worktree("w20b", "feat/s2")
	d.KeepFiles = true
	write(t, filepath.Join(d.Path, "x.txt"), "x")
	if got := f.one(d); got.Outcome != OutcomeHeld || got.Reason != ReasonDirty {
		t.Fatalf("got %+v, want held/dirty even when keeping", got)
	}
}

func TestReconcile_EmptyHuskIsRemovedButAnyFileKeepsIt(t *testing.T) {
	f := newFixture(t)
	f.idle()
	root := filepath.Join(f.root, "proj", "husk")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := f.one(Worktree{ID: "h1", Path: root, State: StateArchived}); got.Outcome != OutcomeRemoved || exists(root) {
		t.Fatalf("got %+v exists=%v, want empty tree removed", got, exists(root))
	}
	keep := filepath.Join(f.root, "proj", "notempty")
	write(t, filepath.Join(keep, "deep", "x.txt"), "x")
	if got := f.one(Worktree{ID: "h2", Path: keep, State: StateArchived}); got.Outcome != OutcomeHeld || !exists(keep) {
		t.Fatalf("got %+v, want held with the file kept", got)
	}
}

// S8: operations on one path serialize.
func TestReclaimer_SerializesOperationsPerPath(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("w21", "feat/t")
	var wg sync.WaitGroup
	results := make([]Result, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r.Outcome == OutcomeError {
			t.Fatalf("concurrent reconcile errored: %+v", r)
		}
	}
}

func TestDiskUsage(t *testing.T) {
	d, err := DiskUsage(filepath.Join(t.TempDir(), "does", "not", "exist"))
	if err != nil || d.TotalBytes <= 0 || d.FreeBytes <= 0 || d.FreeBytes > d.TotalBytes {
		t.Fatalf("DiskUsage = %+v, %v", d, err)
	}
}

// The real lsof probe parses on this OS (no LLM, no network).
func TestProcessCwds_RealProbeSeesThisProcess(t *testing.T) {
	cwds, err := ProcessCwds(context.Background())
	if err != nil {
		t.Skipf("process listing unavailable here: %v", err)
	}
	here, _ := os.Getwd()
	for _, c := range cwds {
		if samePath(c, here) {
			return
		}
	}
	t.Fatalf("this process's cwd %s not among %d entries", here, len(cwds))
}
