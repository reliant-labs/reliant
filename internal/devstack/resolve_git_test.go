package devstack

import (
	"context"
	"os"
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

// TestRelativeProjectDirResolvesLikeAbsolute: forge's commands pass the
// project dir as "." when run from its root (`forge env deploy` renders the
// release bundle from "."), and Worktree must give the answer it gives for the
// absolute path. It used to resolve git's relative --git-common-dir (".git")
// against the relative "." and compare that with the absolute git dir, so a
// PRIMARY checkout came back as a linked worktree keyed on its own basename,
// and every render of that command left the default stack.
func TestRelativeProjectDirResolvesLikeAbsolute(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := filepath.Join(t.TempDir(), "primary-checkout")
	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepoOnBranch(t, primary)
	wt := filepath.Join(t.TempDir(), "wt-feature")
	git(t, primary, "worktree", "add", "-q", "-b", "feature-x", wt)

	for _, tc := range []struct {
		name, cwd, want string
	}{
		{"primary checkout", primary, ""},
		{"linked worktree", wt, "wt-feature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Worktree(tc.cwd); got != tc.want {
				t.Fatalf("Worktree(%q) = %q, want %q", tc.cwd, got, tc.want)
			}
			t.Chdir(tc.cwd)
			for _, rel := range []string{".", ""} {
				if got := Worktree(rel); got != tc.want {
					t.Errorf("Worktree(%q) from %s = %q, want %q (what the absolute path gives)", rel, tc.cwd, got, tc.want)
				}
			}
		})
	}
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
