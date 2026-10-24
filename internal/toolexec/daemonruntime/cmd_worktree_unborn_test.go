package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestHandleWorktreeCreate_UnbornHEAD reproduces the exact reported failure:
// a repo created by `git init` with NO commit (unborn HEAD) — both the
// new-project auto-init path and a user's manual `git init` — previously made
// worktree creation fail with "invalid reference: main". The daemon handler
// must now seed a root commit and create the worktree successfully.
func TestHandleWorktreeCreate_UnbornHEAD(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	// TestMain's git config sets no identity either, and its $HOME keeps the
	// worktree out of the real one.
	t.Parallel()

	projectPath := t.TempDir()
	initCmd := exec.Command("git", "init", "-b", "main", ".")
	initCmd.Dir = projectPath
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v, output: %s", err, out)
	}
	// Sanity: the repo is genuinely commitless before the handler runs.
	if headCmd := exec.Command("git", "rev-parse", "--verify", "HEAD"); func() bool {
		headCmd.Dir = projectPath
		return headCmd.Run() == nil
	}() {
		t.Fatal("expected unborn HEAD (no commits) before worktree create")
	}

	payload, _ := json.Marshal(worktreeCreateRequest{
		ProjectPath: projectPath,
		WorkspaceID: uniqueWorkspaceID(t, "ws-unborn"),
		SubPath:     "",
		Name:        "feature",
		Branch:      "worktree/feature",
	})
	out, err := handleWorktreeCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleWorktreeCreate: %v", err)
	}
	var resp worktreeCreateResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected worktree creation to succeed on commitless repo, got error: %q", resp.Error)
	}
	if _, statErr := os.Stat(filepath.Join(resp.WorktreePath, ".git")); statErr != nil {
		t.Errorf("worktree checkout missing at %s: %v", resp.WorktreePath, statErr)
	}

	// The project repo must now have exactly one commit on main (the seeded
	// root commit), and it must be empty — the fix must not sweep the user's
	// working-tree files into a commit they didn't ask for.
	countCmd := exec.Command("git", "rev-list", "--count", "HEAD")
	countCmd.Dir = projectPath
	if countOut, err := countCmd.Output(); err != nil {
		t.Fatalf("rev-list after create: %v", err)
	} else if got := string(countOut); got != "1\n" {
		t.Errorf("commit count = %q, want \"1\\n\"", got)
	}
}
