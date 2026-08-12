package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHandleCheckGit verifies that the is_git_repo observation is nested-aware.
//
// The flag is a cache of this answer, and CreateProject derives the same flag
// from repo.Discover. A root-only stat here would disagree with that for
// multi-repo projects (root with no .git, children that have one) and the API
// would persist false over the correct value on every GetProject, gating the
// worktree UI behind "Git repository required".
func TestHandleCheckGit(t *testing.T) {
	call := func(t *testing.T, path string) bool {
		t.Helper()
		payload, err := json.Marshal(checkGitRequest{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		out, err := handleCheckGit(context.Background(), payload)
		if err != nil {
			t.Fatalf("handleCheckGit: %v", err)
		}
		var resp checkGitResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("unmarshal resp: %v", err)
		}
		return resp.IsGitRepo
	}

	mkGitDir := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("root is a git repo", func(t *testing.T) {
		dir := t.TempDir()
		mkGitDir(t, dir)
		if !call(t, dir) {
			t.Error("expected true for a plain single git repo")
		}
	})

	t.Run("plain directory is not a git repo", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if call(t, dir) {
			t.Error("expected false for a non-git directory")
		}
	})

	t.Run("multi-repo root with no root .git", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"api", "web"} {
			sub := filepath.Join(dir, name)
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			mkGitDir(t, sub)
		}
		if !call(t, dir) {
			t.Error("expected true: nested repos make this a git project")
		}
	})

	t.Run("removed .git reports false", func(t *testing.T) {
		dir := t.TempDir()
		mkGitDir(t, dir)
		if !call(t, dir) {
			t.Fatal("precondition: expected true before removal")
		}
		if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
			t.Fatal(err)
		}
		// The flag is a bidirectional cache, so this must flip back to false
		// rather than being pinned to a stale true.
		if call(t, dir) {
			t.Error("expected false after .git was removed")
		}
	})

	t.Run("missing path reports false rather than erroring", func(t *testing.T) {
		if call(t, filepath.Join(t.TempDir(), "does-not-exist")) {
			t.Error("expected false for a nonexistent path")
		}
	})
}
