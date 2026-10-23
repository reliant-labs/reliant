package devstack

import (
	"os"
	"path/filepath"
	"testing"
)

// SharedProjectDir is where machine-shared compose infrastructure runs from
// and where the files it mounts are written. The defect it exists to prevent:
// a linked worktree named like the primary (`<container>/<repo>`) ran the
// shared compose project from ITS OWN directory, so compose resolved every
// bind mount there, saw a changed config and recreated the shared
// containers under every other stack.

func TestSharedProjectDirIsThePrimaryFromALinkedWorktree(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	initRepoOnBranch(t, primary)
	// The nested-repo layout that produced the incident: the worktree has
	// the SAME basename as the primary, so compose gives both one project.
	wt := filepath.Join(t.TempDir(), "chat-scroll", filepath.Base(primary))
	git(t, primary, "worktree", "add", "-q", "-b", "feat", wt)

	if got := SharedProjectDir(wt); !sameDir(got, primary) {
		t.Fatalf("SharedProjectDir(worktree) = %q, want the primary checkout %q — a shared stack driven "+
			"from the worktree resolves its bind mounts there and recreates every shared container", got, primary)
	}
	if got := SharedProjectDir(primary); !sameDir(got, primary) {
		t.Errorf("SharedProjectDir(primary) = %q, want itself %q", got, primary)
	}
	if !filepath.IsAbs(SharedProjectDir(wt)) {
		t.Errorf("SharedProjectDir must be absolute: compose stamps it as the project's working_dir")
	}
}

// A forge project nested inside a larger repo maps to the SAME subdirectory
// of the primary, not to the primary's repo root — that is where its compose
// file and its generated config live.
func TestSharedProjectDirKeepsTheProjectsOffsetInTheRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	initRepoOnBranch(t, primary)
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, primary, "worktree", "add", "-q", "-b", "feat", wt)

	sub := filepath.Join("services", "api")
	if err := os.MkdirAll(filepath.Join(wt, sub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(primary, sub), 0o755); err != nil {
		t.Fatal(err) // so sameDir can resolve symlinks (/var vs /private/var) on both sides
	}
	want := filepath.Join(primary, sub)
	if got := SharedProjectDir(filepath.Join(wt, sub)); !sameDir(got, want) {
		t.Errorf("SharedProjectDir(<wt>/%s) = %q, want %q", sub, got, want)
	}
}

func TestSharedProjectDirOutsideARepoIsItself(t *testing.T) {
	gitAvailable(t)
	dir := t.TempDir()
	if got := SharedProjectDir(dir); !sameDir(got, dir) {
		t.Errorf("SharedProjectDir(non-repo) = %q, want %q", got, dir)
	}
}
