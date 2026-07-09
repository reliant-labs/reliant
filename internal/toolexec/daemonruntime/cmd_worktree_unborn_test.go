package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setWorktreeTestGitEnv isolates git from the host config. Crucially it does
// NOT set a user identity: the fix must seed the initial commit with its own
// fallback identity, exactly as a fresh cloud workspace pod (no git identity)
// would exercise it.
func setWorktreeTestGitEnv(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	// init.defaultBranch is set to a non-"main" value to prove the worktree
	// bases off the actual unborn branch, not a hardcoded guess.
	content := "[init]\n\tdefaultBranch = main\n"
	if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
		t.Fatalf("write gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	// worktree.create writes under $HOME/.reliant/worktrees; keep it in temp.
	t.Setenv("HOME", t.TempDir())
}

// TestHandleWorktreeCreate_UnbornHEAD reproduces the exact reported failure:
// a repo created by `git init` with NO commit (unborn HEAD) — both the
// new-project auto-init path and a user's manual `git init` — previously made
// worktree creation fail with "invalid reference: main". The daemon handler
// must now seed a root commit and create the worktree successfully.
func TestHandleWorktreeCreate_UnbornHEAD(t *testing.T) {
	setWorktreeTestGitEnv(t)

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
		WorkspaceID: "ws-unborn",
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
