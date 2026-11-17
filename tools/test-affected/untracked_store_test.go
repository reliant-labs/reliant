package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestPnpmStoreInACheckoutIsNoChange: pnpm puts its content-addressable store
// at the root of a checkout it cannot hard-link into from the user's store (a
// sandboxed clone, another filesystem). pr-fix on reliant #2807 left one of
// ~33,600 files in its worktree; the planner read every one as an untracked
// change no rule claims, ran every lane in full, and the gate timed out. With
// the repository's own .gitignore, the store is no change at all.
func TestPnpmStoreInACheckoutIsNoChange(t *testing.T) {
	testkit.Slow(t, "runs git over a throwaway repository", testkit.Inputs(".gitignore"))
	ignore, err := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	putFixture(t, dir, ".gitignore", string(ignore), 0o644)
	putFixture(t, dir, "README.md", "base\n", 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	putFixture(t, dir, ".pnpm-store/v11/files/00/0a1b2c", "blob\n", 0o644)
	putFixture(t, dir, ".pnpm-store/v11/index/ab/cd-index.json", "{}\n", 0o644)
	putFixture(t, dir, "internal/x/x.go", "package x\n", 0o644)

	changes, _, _, err := diff(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range changes {
		paths = append(paths, c.Path)
		if strings.HasPrefix(c.Path, ".pnpm-store/") {
			t.Errorf("the pnpm store is a change: %s", c.Path)
		}
	}
	if len(paths) != 1 || paths[0] != "internal/x/x.go" {
		t.Errorf("changes = %v, want only the real untracked file internal/x/x.go", paths)
	}
}
