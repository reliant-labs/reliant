// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// Each test builds a real repository with one kind of crash damage — the files
// a VM killed mid-write leaves — and checks RepairRepo repairs exactly that,
// leaves working trees alone, and reports what it could not save.

// later is a clock past every age floor (MinAge, StaleTempAge), so the
// fixtures' fresh files count as old crash debris.
func later() time.Time { return time.Now().Add(7 * time.Hour) }

func repairOpts(f *fixture) RepairOptions {
	return RepairOptions{WorktreeRoot: f.root, Now: later}
}

func gitOK(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

func objectPath(commonDir, id string) string {
	return filepath.Join(commonDir, "objects", id[:2], id[2:])
}

// emptyLoose replaces object id's loose file with a zero-byte one, as a crash
// after the rename but before the data reached the disk leaves it.
func emptyLoose(t *testing.T, commonDir, id string) {
	t.Helper()
	p := objectPath(commonDir, id)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(p, 0o644)
	if err := os.WriteFile(p, nil, 0o444); err != nil {
		t.Fatal(err)
	}
}

func TestRepair_EmptyLooseObjectWithPackedCopyIsRemovedAndNothingIsLost(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	common := filepath.Join(f.project, ".git")
	blob := run(t, f.project, "rev-parse", "HEAD:a.txt")
	run(t, f.project, "repack", "-q", "-a", "-d")
	emptyLoose(t, common, blob)
	if out, _ := gitOK(f.project, "fsck", "--no-dangling"); !strings.Contains(out, "is empty") {
		t.Fatalf("fixture: fsck must see the empty loose object:\n%s", out)
	}

	rep := RepairRepo(context.Background(), common, repairOpts(f))

	if !slices.Equal(rep.EmptyObjects, []string{blob}) || len(rep.LostObjects) != 0 || rep.Unhealthy() {
		t.Fatalf("report = %+v, want the empty object removed and nothing lost", rep)
	}
	if out, err := gitOK(f.project, "cat-file", "-p", blob); err != nil || out != "one" {
		t.Fatalf("object not readable from its pack after repair: %v %s", err, out)
	}
	if out, err := gitOK(f.project, "fsck", "--no-dangling"); err != nil || out != "" {
		t.Fatalf("fsck after repair: %v\n%s", err, out)
	}
}

func TestRepair_EmptyLooseObjectStillReferencedIsReportedLost(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	common := filepath.Join(f.project, ".git")
	write(t, filepath.Join(f.project, "b.txt"), "two\n")
	run(t, f.project, "add", "b.txt")
	run(t, f.project, "commit", "-q", "-m", "b")
	needed := run(t, f.project, "rev-parse", "HEAD:b.txt") // loose, in no pack
	garbage := run(t, f.project, "hash-object", "-w", "--stdin")
	emptyLoose(t, common, needed)
	emptyLoose(t, common, garbage)
	write(t, filepath.Join(common, "gc.log"), "error: object file is empty\n")

	rep := RepairRepo(context.Background(), common, repairOpts(f))

	if len(rep.EmptyObjects) != 2 {
		t.Fatalf("EmptyObjects = %v, want both removed", rep.EmptyObjects)
	}
	if !slices.Equal(rep.LostObjects, []string{needed}) {
		t.Fatalf("LostObjects = %v, want only the referenced %s (the unreferenced one was garbage)", rep.LostObjects, needed)
	}
	if !rep.Unhealthy() || len(rep.FsckProblems) == 0 {
		t.Fatalf("lost history must be reported, not hidden: %+v", rep)
	}
	if !exists(filepath.Join(common, "gc.log")) || rep.RemovedGCLog != "" {
		t.Fatal("gc.log must stay while the repository is still damaged")
	}
}

func TestRepair_StaleGCLogIsRemovedFromAHealthyRepository(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	common := filepath.Join(f.project, ".git")
	write(t, filepath.Join(common, "gc.log"), "warning: There are too many unreachable loose objects\n")

	rep := RepairRepo(context.Background(), common, repairOpts(f))

	if exists(filepath.Join(common, "gc.log")) {
		t.Fatal("gc.log still blocks git gc --auto")
	}
	if !strings.Contains(rep.RemovedGCLog, "too many unreachable") {
		t.Fatalf("RemovedGCLog = %q, want its content kept in the report", rep.RemovedGCLog)
	}
}

func TestRepair_TornWorktreeIndexIsRebuiltWithoutTouchingTheWorkingTree(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		tear func(t *testing.T, index string)
	}{
		{"empty", func(t *testing.T, index string) { write(t, index, "") }},
		{"truncated", func(t *testing.T, index string) {
			b, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			write(t, index, string(b[:len(b)/2]))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			w := f.worktree("w1", "feat/torn")
			write(t, filepath.Join(w.Path, "a.txt"), "edited\n") // unstaged edit
			write(t, filepath.Join(w.Path, "new.txt"), "untracked\n")
			admin := filepath.Join(f.project, ".git", "worktrees", "w1")
			tc.tear(t, filepath.Join(admin, "index"))
			if _, err := gitOK(w.Path, "status", "--porcelain"); err == nil {
				t.Fatal("fixture: a torn index must break git status")
			}

			rep := RepairRepo(context.Background(), filepath.Join(f.project, ".git"), repairOpts(f))

			if len(rep.RebuiltIndexes) != 1 || !strings.HasPrefix(rep.RebuiltIndexes[0], w.Path) || rep.Unhealthy() {
				t.Fatalf("report = %+v, want the index of %s rebuilt", rep, w.Path)
			}
			out, err := gitOK(w.Path, "status", "--porcelain")
			if err != nil {
				t.Fatalf("git status after repair: %v %s", err, out)
			}
			if out != " M a.txt\n?? new.txt" {
				t.Fatalf("status = %q, want the edit unstaged and the new file untracked", out)
			}
			if b, _ := os.ReadFile(filepath.Join(w.Path, "a.txt")); string(b) != "edited\n" {
				t.Fatalf("working tree touched: a.txt = %q", b)
			}
			if exists(filepath.Join(admin, "index.lock")) || exists(filepath.Join(admin, "index.reliant-repair")) {
				t.Fatal("repair left its lock or scratch index behind")
			}
		})
	}
}

func TestRepair_MainCheckoutIndexIsRebuilt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	common := filepath.Join(f.project, ".git")
	write(t, filepath.Join(common, "index"), "")

	rep := RepairRepo(context.Background(), common, repairOpts(f))

	if len(rep.RebuiltIndexes) != 1 {
		t.Fatalf("report = %+v, want the main index rebuilt", rep)
	}
	if out, err := gitOK(f.project, "status", "--porcelain"); err != nil || out != "" {
		t.Fatalf("status after repair = %q, %v; want clean", out, err)
	}
}

func TestRepair_IntactIndexesAndFreshDamageAreLeftAlone(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	w := f.worktree("w1", "feat/ok")
	admin := filepath.Join(f.project, ".git", "worktrees", "w1")
	before, _ := os.ReadFile(filepath.Join(admin, "index"))

	// Fresh: inside MinAge, a damaged index is left for the next pass.
	write(t, filepath.Join(f.project, ".git", "index"), "")
	rep := RepairRepo(context.Background(), filepath.Join(f.project, ".git"), RepairOptions{WorktreeRoot: f.root})
	if rep.Repaired() {
		t.Fatalf("fresh damage must wait out MinAge: %+v", rep)
	}
	after, _ := os.ReadFile(filepath.Join(admin, "index"))
	if string(before) != string(after) {
		t.Fatalf("intact index of %s rewritten", w.Path)
	}
}

func TestRepair_EmptyHEADIsRestoredFromTheReflog(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	w := f.worktree("w1", "feat/head")
	head := run(t, w.Path, "rev-parse", "HEAD")
	admin := filepath.Join(f.project, ".git", "worktrees", "w1")
	write(t, filepath.Join(admin, "HEAD"), "")
	write(t, filepath.Join(admin, "ORIG_HEAD"), "")

	rep := RepairRepo(context.Background(), filepath.Join(f.project, ".git"), repairOpts(f))

	if len(rep.RestoredHEADs) != 1 || !strings.Contains(rep.RestoredHEADs[0], head) {
		t.Fatalf("report = %+v, want HEAD restored to %s", rep, head)
	}
	if got := run(t, w.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want %s", got, head)
	}
	if exists(filepath.Join(admin, "ORIG_HEAD")) {
		t.Fatal("empty ORIG_HEAD not removed")
	}
}

func TestRepair_StaleTempPacksAndLocksAreRemoved(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	common := filepath.Join(f.project, ".git")
	tmpPack := filepath.Join(common, "objects", "pack", "tmp_pack_abc123")
	write(t, tmpPack, "partial pack")
	lock := filepath.Join(common, "index.lock")
	write(t, lock, "")

	rep := RepairRepo(context.Background(), common, repairOpts(f))

	if exists(tmpPack) || exists(lock) || len(rep.RemovedDebris) != 2 {
		t.Fatalf("report = %+v, want the stale tmp pack and index.lock removed", rep)
	}

	// Inside StaleTempAge they belong to a live process.
	write(t, tmpPack, "partial pack")
	RepairRepo(context.Background(), common, RepairOptions{WorktreeRoot: f.root})
	if !exists(tmpPack) {
		t.Fatal("a fresh tmp pack (a fetch in flight) was removed")
	}
}

func TestRepair_RegistrationsOfGoneCheckoutsArePrunedOnlyWhenReliantOwnsThem(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	gone := f.worktree("gone", "feat/gone")          // reliant lock, directory removed
	foreign := f.worktree("foreign", "feat/foreign") // user lock, directory removed
	run(t, f.project, "worktree", "unlock", foreign.Path)
	run(t, f.project, "worktree", "lock", "--reason", "on my USB disk", foreign.Path)
	torn := f.worktree("torn", "feat/torn") // empty lock (crash during lock), removed
	write(t, filepath.Join(f.project, ".git", "worktrees", "torn", "locked"), "")
	live := f.worktree("live", "feat/live") // still there
	for _, p := range []string{gone.Path, foreign.Path, torn.Path} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
	admins := filepath.Join(f.project, ".git", "worktrees")

	rep := RepairRepo(context.Background(), filepath.Join(f.project, ".git"), repairOpts(f))

	if exists(filepath.Join(admins, "gone")) || exists(filepath.Join(admins, "torn")) {
		t.Fatalf("reliant's dead registrations not pruned: %+v", rep)
	}
	if len(rep.PrunedRegistrations) != 2 {
		t.Fatalf("PrunedRegistrations = %v, want gone and torn", rep.PrunedRegistrations)
	}
	if !exists(filepath.Join(admins, "foreign", "locked")) || len(rep.SkippedRegistrations) != 1 ||
		!strings.Contains(rep.SkippedRegistrations[0], "on my USB disk") {
		t.Fatalf("a user's lock must be left and reported: %+v", rep)
	}
	if !exists(filepath.Join(admins, "live")) {
		t.Fatal("a live registration was pruned")
	}
	if out := run(t, f.project, "worktree", "list", "--porcelain"); strings.Contains(out, "/gone") || !strings.Contains(out, live.Path) {
		t.Fatalf("git's view after repair:\n%s", out)
	}
}

func TestRepair_MissingRootPrunesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.worktree("w1", "feat/a")
	// The whole worktree volume is missing: unmounted, not deleted.
	if err := os.RemoveAll(f.root); err != nil {
		t.Fatal(err)
	}
	rep := RepairRepo(context.Background(), filepath.Join(f.project, ".git"), repairOpts(f))
	if len(rep.PrunedRegistrations) != 0 || !exists(filepath.Join(f.project, ".git", "worktrees", "w1")) {
		t.Fatalf("pruned with the root missing: %+v", rep)
	}
}

func TestRepair_BusyCheckoutIsNotPruned(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.worktree("w1", "feat/a")
	if err := os.RemoveAll(w.Path); err != nil {
		t.Fatal(err)
	}
	opts := repairOpts(f)
	opts.Busy = func(c string) bool { return c == w.Path }
	if rep := RepairRepo(context.Background(), filepath.Join(f.project, ".git"), opts); len(rep.PrunedRegistrations) != 0 {
		t.Fatalf("pruned a checkout being worked on: %+v", rep)
	}
}

func TestDiscoverCommonDirs(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	f.worktree("w1", "feat/a")
	f.worktree("w2", "feat/b")
	got := DiscoverCommonDirs(f.root)
	want := resolvePath(filepath.Join(f.project, ".git"))
	if len(got) != 1 || resolvePath(got[0]) != want {
		t.Fatalf("DiscoverCommonDirs = %v, want [%s]", got, want)
	}
}

// TestReconcile_RepairsTheParentRepositoryBeforeReadingIt: the sweep is what
// reaches a damaged repository between daemon starts, so it repairs first —
// once per RepairInterval — and reports through OnRepair.
func TestReconcile_RepairsTheParentRepositoryBeforeReadingIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.idle()
	w := f.worktree("w1", "feat/a")
	w.State = StateActive
	w.Checkouts = []Checkout{{RepoPath: f.project}}
	common := filepath.Join(f.project, ".git")
	write(t, filepath.Join(common, "objects", "pack", "tmp_pack_x"), "torn")
	var reports []RepairReport
	f.r.OnRepair = func(r RepairReport) { reports = append(reports, r) }

	f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}})
	if len(reports) != 1 || len(reports[0].RemovedDebris) != 1 {
		t.Fatalf("reports = %+v, want one repair of the parent repository", reports)
	}

	write(t, filepath.Join(common, "objects", "pack", "tmp_pack_y"), "torn")
	f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}})
	if len(reports) != 1 {
		t.Fatal("repaired again inside RepairInterval")
	}
}
