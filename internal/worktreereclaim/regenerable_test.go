// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// Regenerable tool output in a checkout does not hold it: forge's scratch and
// generate cache, a project's pinned tools, and its scratch tmp/.
func TestReconcile_ToolOutputIsRegenerable(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds 7 git fixtures; TestRebuildablePath_Entries covers the rule under -short")
	}
	// One fixture per case, in parallel: the seven in a row on one fixture
	// were this package's critical path (157s of a 226s run at load ~100,
	// against the race-enabled release gate's 5-minute package timeout).
	for name, rel := range map[string]string{
		"forge-tmp":            ".forge/tmp/render-123/main.k",
		"forge-generate-cache": ".forge/generate-cache.json",
		"tools-bin":            ".tools/bin/golangci-lint",
		"tools-stamps":         ".tools/stamps/buf-v1.50.0",
		"tools-tmp":            ".tools/tmp/install.log",
		"tmp":                  "tmp/notes.md",
		"tmp-ci":               "tmp/ci/summary.txt",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.idle()
			ignore(t, f, ".forge/*\n.tools/\ntmp/\n")
			w := f.worktree("regen-"+name, "feat/regen-"+name)
			write(t, filepath.Join(w.Path, filepath.FromSlash(rel)), "x")
			if got := f.one(w); got.Outcome != OutcomeRemoved || exists(w.Path) {
				t.Fatalf("%s (%s): got %+v, want removed", name, rel, got)
			}
		})
	}
}

// The root-anchored entries do not reach a same-named directory deeper in the
// tree, and data git lists inside tmp/ (one with a tracked file, so git does
// not collapse it) is still data.
func TestReconcile_RegenerableEntriesDoNotCoverData(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds 3 git fixtures; TestRebuildablePath_Entries covers the rule under -short")
	}
	f := newFixture(t)
	f.idle()
	ignore(t, f, "tmp/*\n!tmp/.keep\nweird/\n")
	write(t, filepath.Join(f.project, "tmp", ".keep"), "")
	run(t, f.project, "add", "tmp/.keep")
	run(t, f.project, "commit", "-q", "-m", "keep tmp")
	run(t, f.project, "push", "-q", "origin", "main")
	for name, rel := range map[string]string{
		"data-in-tmp":     "tmp/data/rows.csv",
		"env-in-tmp":      "tmp/.env",
		"tmp-not-at-root": "weird/tmp/thing.bin",
	} {
		w := f.worktree("keep-"+name, "feat/keep-"+name)
		write(t, filepath.Join(w.Path, filepath.FromSlash(rel)), "x")
		if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonData || !exists(w.Path) {
			t.Fatalf("%s (%s): got %+v, want held/data", name, rel, got)
		}
	}
}

func TestRebuildablePath_Entries(t *testing.T) {
	t.Parallel()
	for p, want := range map[string]bool{
		".forge/tmp/":                  true,
		".forge/tmp/x/y":               true,
		".forge/generate-cache.json":   true,
		".tools/":                      true,
		".tools/stamps/buf":            true,
		".tools/pre-commit/repoabc/":   true,
		"tmp/":                         true,
		"tmp/ci/a.txt":                 true,
		"web/tmp/x":                    false, // tmp is anchored at the checkout root
		"sub/.tools/state.json":        false, // so is .tools
		"tmp/data/x":                   false, // data under tmp is data
		".tools/.env":                  false,
		".forge/hostinfra/pg/data":     false,
		".forge/generate-cache.json.x": false,
	} {
		if got := rebuildablePath(p); got != want {
			t.Errorf("rebuildablePath(%q) = %v, want %v", p, got, want)
		}
	}
}

// preCommitClone makes a pre-commit-style hook clone (a full repository with
// its own history) at rel inside the checkout.
func preCommitClone(t *testing.T, checkout, rel string) string {
	t.Helper()
	clone := filepath.Join(checkout, filepath.FromSlash(rel))
	write(t, filepath.Join(clone, ".pre-commit-hooks.yaml"), "- id: x\n")
	run(t, clone, "init", "-q", "-b", "main")
	run(t, clone, "add", ".")
	run(t, clone, "commit", "-q", "-m", "hooks")
	return clone
}

// pre-commit's hook clones under <checkout>/.tools/pre-commit are regenerable:
// they do not make the checkout a nested repository, and removal takes them.
func TestReconcile_PreCommitClonesUnderToolsAreRegenerable(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~48 git processes")
	f := newFixture(t)
	f.idle()
	ignore(t, f, ".tools/\n")
	w := f.worktree("precommit", "feat/precommit")
	preCommitClone(t, w.Path, ".tools/pre-commit/repo1a2b3c")
	preCommitClone(t, w.Path, ".tools/pre-commit/repo4d5e6f")
	write(t, filepath.Join(w.Path, ".tools", "pre-commit", "db.db"), "pre-commit's own index")
	if got := f.one(w); got.Outcome != OutcomeHeld || !exists(filepath.Join(w.Path, ".tools", "pre-commit", "db.db")) {
		t.Fatalf("nested clone/protected db layout must hold whole workspace: %+v", got)
	}
}

// Only an IGNORED clone directly under .tools/pre-commit qualifies. A clone
// git does not ignore, a clone deeper down, and a repository anywhere else
// (including the now-regenerable tmp/) is still a nested repository.
func TestReconcile_OtherNestedRepositoriesStillHold(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds 5 git fixtures; TestRebuildablePath_Entries covers the rule under -short")
	}
	for name, tc := range map[string]struct{ ignore, rel string }{
		"not-ignored":   {"tmp/\n", ".tools/pre-commit/repo1"},
		"too-deep":      {".tools/\n", ".tools/pre-commit/repo1/sub"},
		"elsewhere":     {".tools/\n", ".tools/other/repo1"},
		"in-tmp":        {"tmp/\n", "tmp/pre-commit/repo1"},
		"pre-commit-at": {".tools/\n", ".tools/pre-commit"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.idle()
			ignore(t, f, tc.ignore)
			w := f.worktree("nest-"+name, "feat/nest-"+name)
			clone := preCommitClone(t, w.Path, tc.rel)
			got := f.one(w)
			if got.Outcome != OutcomeHeld || !exists(clone) {
				t.Fatalf("got %+v, want held with the clone kept", got)
			}
			if got.Reason != ReasonNestedRepo {
				t.Fatalf("got %+v, want nested-repository", got)
			}
		})
	}
}

func TestToolClone_NeedsAnOwnerThatIgnoresIt(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	ignore(t, f, ".tools/\n")
	w := f.worktree("owner", "feat/owner")
	clone := preCommitClone(t, w.Path, ".tools/pre-commit/repoX")
	ctx := context.Background()
	if !toolClone(ctx, clone, []string{w.Path, clone}) {
		t.Fatal("an ignored pre-commit clone of its checkout is a tool clone")
	}
	if toolClone(ctx, clone, []string{clone}) {
		t.Fatal("a repository with no containing checkout is never a tool clone")
	}
	if got := scanWorkspace(ctx, w.Path); len(got.Repos) != 1 || got.Repos[0] != w.Path {
		t.Fatalf("scan repos = %v, want only the checkout", got.Repos)
	}
}
