package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

func rcGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func newRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rcGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rcGit(t, dir, "add", ".")
	rcGit(t, dir, "commit", "-q", "-m", "init")
}

func recreate(t *testing.T, req worktreeRecreateRequest) worktreeRecreateResponse {
	t.Helper()
	payload, _ := json.Marshal(req)
	raw, err := handleWorktreeRecreate(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	var resp worktreeRecreateResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// F3: when the second repo fails, the first checkout is removed again, from its
// own repository (its parent directory is not a git repo).
func TestRecreate_PartialFailureRollsBackEarlierCheckouts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	api, web := filepath.Join(base, "api"), filepath.Join(base, "web")
	newRepo(t, api)
	newRepo(t, web)
	rcGit(t, api, "branch", "feat/x")
	rcGit(t, web, "branch", "feat/x")
	ws := filepath.Join(base, "ws")
	// Make web's add fail after api's succeeded: its destination is a file.
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "web"), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The pre-check refuses an existing path, so create the blocker after it:
	// use a nested rel whose parent cannot be created.
	if err := os.Remove(filepath.Join(ws, "web")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := recreate(t, worktreeRecreateRequest{
		WorktreePath: ws, Branch: "feat/x", WorktreeID: "id1",
		Repos: []worktreeRecreateRepo{{RepoPath: api, Rel: "api"}, {RepoPath: web, Rel: "blocker/web"}},
	})
	if resp.Success {
		t.Fatalf("expected failure, got %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(ws, "api")); !os.IsNotExist(err) {
		t.Errorf("api checkout was left behind")
	}
	if out := rcGit(t, api, "worktree", "list", "--porcelain"); strings.Contains(out, "ws/api") {
		t.Errorf("api still registers the rolled-back checkout:\n%s", out)
	}
}

// A checkout parked by an interrupted clean-up is moved back by recreate, which
// would otherwise fail: its branch is still checked out in quarantine.
func TestRecreate_MovesAParkedCheckoutBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	ws := filepath.Join(base, "ws")
	rcGit(t, repo, "worktree", "add", "-q", "-b", "feat/p", ws, "main")
	rc, err := sharedWorktreeReclaimer()
	if err != nil {
		t.Fatal(err)
	}
	q := filepath.Join(rc.Root, ".reclaim", "id2", "root-1-aa")
	if err := os.MkdirAll(filepath.Dir(q), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(q+".origin", []byte(ws), 0o644); err != nil {
		t.Fatal(err)
	}
	rcGit(t, repo, "worktree", "move", ws, q)
	if err := worktreereclaim.LockCheckout(context.Background(), q, "id2"); err != nil {
		t.Fatal(err)
	}
	resp := recreate(t, worktreeRecreateRequest{
		WorktreePath: ws, Branch: "feat/p", WorktreeID: "id2", Fence: "5",
		Repos: []worktreeRecreateRepo{{RepoPath: repo, Rel: ""}},
	})
	if !resp.Success {
		t.Fatalf("recreate failed: %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		t.Errorf("checkout not back at its path: %v", err)
	}
	if len(resp.RestoredFromQuarantine) != 1 {
		t.Errorf("RestoredFromQuarantine = %v", resp.RestoredFromQuarantine)
	}
}

// A checkout whose directory vanished without git being told — deleted by a
// user, a GC, a crash — is still registered, and reliant LOCKS its checkouts,
// so `git worktree prune` keeps the registration too. `git worktree add` then
// refuses the path ("missing but locked worktree") and the branch ("already
// checked out"), so recreate could never rebuild it. The stale registration of
// a path that is gone is dropped first.
func TestRecreate_RebuildsACheckoutDeletedOutFromUnderGit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	ws := filepath.Join(base, "ws")
	rcGit(t, repo, "worktree", "add", "-q", "-b", "feat/gone", ws, "main")
	if err := worktreereclaim.LockCheckout(context.Background(), ws, "id3"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}

	resp := recreate(t, worktreeRecreateRequest{
		WorktreePath: ws, Branch: "feat/gone", WorktreeID: "id3",
		Repos: []worktreeRecreateRepo{{RepoPath: repo, Rel: ""}},
	})
	if !resp.Success {
		t.Fatalf("recreate failed: %+v", resp)
	}
	if got := strings.TrimSpace(rcGit(t, ws, "rev-parse", "--abbrev-ref", "HEAD")); got != "feat/gone" {
		t.Errorf("recreated checkout is on %q, want feat/gone", got)
	}
}

// A branch that exists only on the remote (deleted locally, or never fetched
// into a local branch) is still recoverable: git creates the local branch
// tracking it.
func TestRecreate_UsesARemoteOnlyBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	newRepo(t, origin)
	rcGit(t, origin, "branch", "feat/pushed")
	repo := filepath.Join(base, "clone")
	rcGit(t, base, "clone", "-q", origin, repo)

	ws := filepath.Join(base, "ws")
	resp := recreate(t, worktreeRecreateRequest{
		WorktreePath: ws, Branch: "feat/pushed",
		Repos: []worktreeRecreateRepo{{RepoPath: repo, Rel: ""}},
	})
	if !resp.Success {
		t.Fatalf("recreate failed: %+v", resp)
	}
	if got := strings.TrimSpace(rcGit(t, ws, "rev-parse", "--abbrev-ref", "HEAD")); got != "feat/pushed" {
		t.Errorf("recreated checkout is on %q, want feat/pushed", got)
	}
}
