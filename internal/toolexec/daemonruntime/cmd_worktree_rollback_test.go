package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A failed `git worktree add` must not leave its directory behind.
//
// ── The bug ──
//
// handleWorktreeCreate runs os.MkdirAll BEFORE git. When git then failed, the
// directory survived: an empty `~/.reliant/worktrees/<id>/` with no .git in
// it, which every later stat reads as a real workspace.
//
// The reproducer was a branch named `release` in a repo that already had
// `release/compute-tier-redesign`. Git stores refs as files, so
// `refs/heads/release` cannot exist beside `refs/heads/release/...`:
//
//	fatal: cannot lock ref 'refs/heads/release':
//	  'refs/heads/release/compute-tier-redesign' exists
//
// The empty directory plus a DB row with no path is what produced
// "project … has no workspace path to scope this request to" on every
// filesystem RPC for that chat.

// gitRepoWithConflictingRef builds a repo whose `release/x` branch makes a
// plain `release` branch impossible — the real-world failure, not a synthetic
// error injection.
func gitRepoWithConflictingRef(t *testing.T) string {
	t.Helper()
	// The ref that makes `release` unusable as a branch name.
	return diagnoseRepo(t, "release/compute-tier-redesign")
}

func TestWorktreeCreate_FailedGitLeavesNoDirectoryBehind(t *testing.T) {
	t.Parallel()
	projectPath := gitRepoWithConflictingRef(t)

	// handleWorktreeCreate builds the path under $HOME — TestMain's, which
	// other tests share. So the workspace is nested, as a real one is
	// (<project>/<name>), under a parent no other test writes to: that
	// parent stays empty, and a rollback that swept one level too far would
	// remove it. Directly under the shared .../worktrees, other tests'
	// checkouts would keep it non-empty and the assertion would pass anyway.
	parentID := uniqueWorkspaceID(t, "proj")

	// MULTI-REPO shape (sub_path set), which is what the real failure was:
	// the project has control-plane/, forge/ and reliant/ repos, so
	// MkdirAll creates the workspace ROOT as the parent of the checkout. That
	// root is the directory that was orphaned.
	//
	// This matters for what the test proves. With sub_path "" the parent is
	// the shared .../worktrees dir and the leaf is git's to create, so no
	// directory is left behind whether or not the rollback exists — an
	// assertion there passes vacuously. An earlier draft of this test did
	// exactly that and survived deleting the fix.
	workspaceID := parentID + "/ws-rollback-test"
	payload, err := json.Marshal(map[string]any{
		"project_path": projectPath,
		"workspace_id": workspaceID,
		"sub_path":     "control-plane",
		"name":         "release",
		"branch":       "release", // collides with release/compute-tier-redesign
		"base_branch":  "main",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	raw, err := handleWorktreeCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("handler returned a transport error: %v", err)
	}

	var resp struct {
		Success      bool   `json:"success"`
		WorktreePath string `json:"worktree_path"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.Success {
		t.Fatal("expected the create to FAIL: `release` cannot be a branch when " +
			"`release/compute-tier-redesign` exists")
	}

	// THE ASSERTION. The workspace root MkdirAll made must be gone.
	workspaceRoot := testWorktreePath(t, workspaceID)
	if _, statErr := os.Stat(workspaceRoot); !os.IsNotExist(statErr) {
		entries, _ := os.ReadDir(workspaceRoot)
		t.Fatalf("a failed create left %s behind (%d entries) — an empty directory with no .git "+
			"reads as a real workspace to everything downstream, which is how a chat ends up "+
			"bound to a worktree that cannot be opened", workspaceRoot, len(entries))
	}

	// The shared parent must survive: it holds the project's other
	// workspaces.
	sharedParent := testWorktreePath(t, parentID)
	if _, statErr := os.Stat(sharedParent); statErr != nil {
		t.Fatalf("rollback removed the SHARED directory %s above the workspace root: %v — that "+
			"would take every other workspace in it along", sharedParent, statErr)
	}
}

// A NON-empty directory is never removed. git may have written a partial
// checkout before failing, and under --force the directory can predate the
// call entirely — deleting it could destroy work this function did not create.
func TestWorktreeCreate_FailedGitKeepsNonEmptyDirectory(t *testing.T) {
	t.Parallel()
	projectPath := gitRepoWithConflictingRef(t)

	workspaceID := uniqueWorkspaceID(t, "ws-preexisting")
	worktreePath := filepath.Join(testWorktreePath(t, workspaceID), "control-plane")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("pre-create: %v", err)
	}
	// Somebody else's file, already there before this call.
	keep := filepath.Join(worktreePath, "precious.txt")
	if err := os.WriteFile(keep, []byte("do not delete\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	payload, _ := json.Marshal(map[string]any{
		"project_path": projectPath,
		"workspace_id": workspaceID,
		"sub_path":     "control-plane",
		"name":         "release",
		"branch":       "release",
		"base_branch":  "main",
	})

	if _, err := handleWorktreeCreate(context.Background(), payload); err != nil {
		t.Fatalf("handler: %v", err)
	}

	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("rollback deleted a file it did not create (%s): %v — the cleanup must only "+
			"remove a directory it left EMPTY", keep, err)
	}
}

// The happy path still works, so the rollback cannot pass by breaking creates.
func TestWorktreeCreate_SucceedsForANonConflictingBranch(t *testing.T) {
	t.Parallel()
	projectPath := gitRepoWithConflictingRef(t)

	workspaceID := uniqueWorkspaceID(t, "ws-happy")
	payload, _ := json.Marshal(map[string]any{
		"project_path": projectPath,
		"workspace_id": workspaceID,
		"sub_path":     "control-plane",
		"name":         "feature",
		"branch":       "feature/ok",
		"base_branch":  "main",
	})

	raw, err := handleWorktreeCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var resp struct {
		Success      bool   `json:"success"`
		WorktreePath string `json:"worktree_path"`
		Error        string `json:"error"`
	}
	_ = json.Unmarshal(raw, &resp)

	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(resp.WorktreePath, ".git")); err != nil {
		t.Fatalf("a successful create must leave a real git worktree at %s: %v", resp.WorktreePath, err)
	}
}
