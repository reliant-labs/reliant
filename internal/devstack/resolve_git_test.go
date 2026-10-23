package devstack

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// git runs a git command in dir, failing the test on error.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// initRepoOnBranch makes a primary git checkout in dir, with one commit, on
// a NAMED feature branch (not the default).
func initRepoOnBranch(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "test")
	git(t, dir, "checkout", "-q", "-b", "chore/bump-forge-93067142")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
}

// TestPrimaryCheckoutOnNamedBranchWorktreeEmpty is THE regression-lock: a
// primary checkout on a feature branch resolves option("worktree") == "" so
// a KCL keying on worktree stays the DEFAULT stack (byte-identical ports).
// The branch fact is still reported (for a consumer that wants it) but it
// must NOT leak into worktree.
func TestPrimaryCheckoutOnNamedBranchWorktreeEmpty(t *testing.T) {
	gitAvailable(t)
	dir := t.TempDir()
	initRepoOnBranch(t, dir)

	if wt := Worktree(dir); wt != "" {
		t.Fatalf("primary checkout worktree = %q, want \"\" (the branch must not leak in)", wt)
	}
	// Branch is reported, sanitized + bounded to 24 chars.
	if b := Branch(dir); b != "chore-bump-forge-9306714" {
		t.Errorf("primary checkout branch = %q, want chore-bump-forge-9306714", b)
	}
	// Options resolves both at once.
	o := Resolve(dir)
	if o.Worktree != "" || o.Branch != "chore-bump-forge-9306714" {
		t.Errorf("Resolve = %+v, want {Worktree:\"\" Branch:chore-bump-forge-9306714}", o)
	}
}

// TestLinkedWorktreeResolvesFromDirBasename: a `git worktree add`'ed checkout
// (NOT the primary) sets option("worktree") to the worktree dir basename —
// the multi-worktree workflow. The SAME repo's primary stays "".
func TestLinkedWorktreeResolvesFromDirBasename(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	initRepoOnBranch(t, primary)

	// Add a linked worktree at <tmp>/wt-feature on a new branch.
	wtDir := filepath.Join(t.TempDir(), "wt-feature")
	git(t, primary, "worktree", "add", "-q", "-b", "feature-x", wtDir)

	// The primary checkout still has empty worktree.
	if wt := Worktree(primary); wt != "" {
		t.Fatalf("primary worktree = %q, want \"\"", wt)
	}

	// The linked worktree resolves to its directory basename "wt-feature".
	if wt := Worktree(wtDir); wt != "wt-feature" {
		t.Fatalf("linked worktree = %q, want %q (from dir basename)", wt, "wt-feature")
	}
	// And its branch fact is the new branch.
	if b := Branch(wtDir); b != "feature-x" {
		t.Errorf("linked worktree branch = %q, want feature-x", b)
	}
}

// TestNonRepoResolvesEmpty: outside any git repo → both facts "".
func TestNonRepoResolvesEmpty(t *testing.T) {
	gitAvailable(t)
	dir := t.TempDir() // not git-init'ed
	o := Resolve(dir)
	if o.Worktree != "" || o.Branch != "" {
		t.Errorf("non-repo Resolve = %+v, want both empty", o)
	}
}
