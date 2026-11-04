// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// stripFixture is an idle fixture whose main ignores the usual build output.
func stripFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.idle()
	ignore(t, f, "node_modules/\ndist/\n.tools/\ntmp/\ndata/\n.forge/*\n")
	return f
}

// fill writes regenerable bulk into a checkout: dependencies, build output,
// tool caches with a pre-commit clone, forge's scratch and tmp/.
func fill(t *testing.T, checkout string) []string {
	t.Helper()
	write(t, filepath.Join(checkout, "web", "node_modules", "pkg", "index.js"), "module.exports = 1\n")
	write(t, filepath.Join(checkout, "dist", "app.js"), "built\n")
	write(t, filepath.Join(checkout, ".tools", "bin", "tool"), "binary\n")
	preCommitClone(t, checkout, ".tools/pre-commit/repo1")
	write(t, filepath.Join(checkout, ".forge", "tmp", "render", "x.k"), "x\n")
	write(t, filepath.Join(checkout, "tmp", "notes.md"), "scratch\n")
	return []string{"web/node_modules", "dist", ".tools", ".forge/tmp", "tmp"}
}

// A held worktree retains its entire contents; automatic cleanup is whole
// safe worktrees only, never deletion of subsets beside unique data.
func TestReconcile_HeldWorktreePreservesRegenerableFiles(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := stripFixture(t)
	w := f.worktree("shed", "feat/shed")
	bulk := fill(t, w.Path)
	write(t, filepath.Join(w.Path, "wip.txt"), "unsaved work\n")
	write(t, filepath.Join(w.Path, "data", "rows.csv"), "1,2\n")
	write(t, filepath.Join(w.Path, ".forge", "hostinfra", "pg", "PG_VERSION"), "16\n")

	got := f.one(w)
	if got.Outcome != OutcomeHeld || (got.Reason != ReasonData && got.Reason != ReasonDirty) {
		t.Fatalf("got %+v, want held for its data or its work", got)
	}
	for _, p := range bulk {
		if !exists(filepath.Join(w.Path, filepath.FromSlash(p))) {
			t.Errorf("%s was removed from held workspace", p)
		}
	}
	if got.StrippedBytes != 0 || len(got.Stripped) != 0 {
		t.Errorf("held tree reported deletion: %+v", got)
	}
	for _, p := range []string{"wip.txt", "a.txt", "data/rows.csv", ".forge/hostinfra/pg/PG_VERSION"} {
		if !exists(filepath.Join(w.Path, filepath.FromSlash(p))) {
			t.Errorf("%s was deleted; only regenerable files may be", p)
		}
	}

	// Unarchiving still works: the checkout is re-locked as live, files intact.
	w.State = StateActive
	w.Checkouts = []Checkout{{RepoPath: f.project}}
	if res := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; res.Outcome != OutcomeLocked {
		t.Fatalf("restore after shedding: got %+v, want locked", res)
	}
	if !exists(filepath.Join(w.Path, "wip.txt")) {
		t.Fatal("the work did not survive the restore")
	}
}

// Shedding never deletes a directory that holds a repository: a nested repo
// inside node_modules keeps node_modules, while dist still goes.
func TestReconcile_ShedSkipsPathsHoldingARepository(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := stripFixture(t)
	w := f.worktree("shed-nested", "feat/shed-nested")
	write(t, filepath.Join(w.Path, "dist", "app.js"), "built\n")
	nested := filepath.Join(w.Path, "node_modules", "dep")
	write(t, filepath.Join(nested, "x.js"), "only copy\n")
	run(t, nested, "init", "-q", "-b", "main")
	run(t, nested, "add", ".")
	run(t, nested, "commit", "-q", "-m", "only copy")

	got := f.one(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonNestedRepo {
		t.Fatalf("got %+v, want held/nested-repository", got)
	}
	if !exists(nested) {
		t.Fatal("a nested repository was deleted")
	}
	if !exists(filepath.Join(w.Path, "dist", "app.js")) {
		t.Fatal("held workspace's build output must remain intact")
	}
}

// Nothing is shed when the hold is about whose the directory is, when the user
// keeps everything, while it is recently active, or for a live worktree.
func TestReconcile_ShedOnlyForGenuineHoldsOfIdleArchivedWorktrees(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds 5 git fixtures; skipped under -short")
	}
	cases := map[string]func(f *fixture, w *Worktree){
		"keep-everything": func(f *fixture, w *Worktree) { w.KeepFiles = true },
		"recently-active": func(f *fixture, w *Worktree) { f.r.Now = nil },
		"in-use": func(f *fixture, w *Worktree) {
			path := w.Path
			f.r.Cwds = func(context.Context) ([]string, error) { return []string{path}, nil }
		},
		"unmanaged": func(f *fixture, w *Worktree) {
			run(f.t, f.project, "worktree", "unlock", w.Path)
			run(f.t, f.project, "worktree", "lock", "--reason", "someone else", w.Path)
		},
		"active": func(f *fixture, w *Worktree) { w.State = StateActive },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := stripFixture(t)
			w := f.worktree("keep-"+name, "feat/keep-"+name)
			fill(t, w.Path)
			write(t, filepath.Join(w.Path, "wip.txt"), "unsaved work\n")
			mutate(f, &w)
			got := f.one(w)
			if len(got.Stripped) != 0 || got.StrippedBytes != 0 {
				t.Fatalf("got %+v, want nothing stripped", got)
			}
			if !exists(filepath.Join(w.Path, "web", "node_modules", "pkg", "index.js")) {
				t.Fatal("node_modules was deleted")
			}
		})
	}
}
