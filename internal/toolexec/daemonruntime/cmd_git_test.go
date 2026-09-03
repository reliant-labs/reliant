// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setGitCloneTestEnv isolates git from the host config, mirroring
// setWorktreeTestGitEnv in cmd_worktree_unborn_test.go.
func setGitCloneTestEnv(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	content := "[init]\n\tdefaultBranch = main\n[user]\n\tname = Test\n\temail = test@example.com\n"
	if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
		t.Fatalf("write gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// newTestOriginRepo creates a bare repo at dir/origin.git with one commit on
// main, and returns its filesystem path (usable as a git.clone "repo" URL).
func newTestOriginRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	originPath := filepath.Join(root, "origin.git")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main", "--bare", originPath)

	seedDir := filepath.Join(root, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seedRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = seedDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	seedRun("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedRun("add", "README.md")
	seedRun("commit", "-q", "-m", "initial")
	seedRun("remote", "add", "origin", originPath)
	seedRun("push", "-q", "origin", "main")

	return originPath
}

// A redelivered git.clone (JetStream WorkQueue redelivery after a dispatch
// timeout/NAK) must not fail just because the FIRST delivery already
// completed the clone on disk. Without the idempotency guard, `git clone`
// itself refuses to run into an existing non-empty directory and the retry
// reports failure even though req.Path is a valid, complete clone.
func TestHandleGitClone_RedeliveryAfterSuccessIsIdempotent(t *testing.T) {
	setGitCloneTestEnv(t)
	origin := newTestOriginRepo(t)
	dest := filepath.Join(t.TempDir(), "dest")

	payload, err := json.Marshal(gitCloneRequest{
		Repo:   origin,
		Branch: "main",
		Path:   dest,
	})
	if err != nil {
		t.Fatal(err)
	}

	// First delivery: real clone.
	out, err := handleGitClone(context.Background(), payload)
	if err != nil {
		t.Fatalf("first handleGitClone: %v", err)
	}
	var resp gitCloneResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Success || resp.Path != dest {
		t.Fatalf("unexpected first response: %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("expected cloned file present: %v", err)
	}

	// Second delivery: same request, same payload — simulates JetStream
	// redelivering the pending command after the gateway's first dispatch
	// attempt timed out even though the daemon had already finished.
	out2, err := handleGitClone(context.Background(), payload)
	if err != nil {
		t.Fatalf("redelivered handleGitClone must succeed idempotently, got error: %v", err)
	}
	var resp2 gitCloneResponse
	if err := json.Unmarshal(out2, &resp2); err != nil {
		t.Fatal(err)
	}
	if !resp2.Success || resp2.Path != dest {
		t.Fatalf("unexpected redelivery response: %+v", resp2)
	}
}

// A genuine conflict — a non-git, non-empty directory already occupying the
// clone destination — must still fail loudly rather than being silently
// treated as an already-completed clone.
func TestHandleGitClone_NonGitConflictStillFails(t *testing.T) {
	setGitCloneTestEnv(t)
	origin := newTestOriginRepo(t)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "unrelated.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(gitCloneRequest{
		Repo:   origin,
		Branch: "main",
		Path:   dest,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := handleGitClone(context.Background(), payload); err == nil {
		t.Fatal("expected handleGitClone to fail when the destination is a non-git, non-empty directory")
	}
}
