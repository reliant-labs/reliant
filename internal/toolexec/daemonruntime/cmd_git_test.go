// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	// Refused, not clobbered: the user's file is untouched.
	if _, err := os.Stat(filepath.Join(dest, "unrelated.txt")); err != nil {
		t.Fatalf("a refused clone must leave the existing directory alone: %v", err)
	}
}

// A clone that is KILLED mid-way must leave nothing at the project path, and
// its redelivery must not be reported as a finished clone.
//
// git cleans up its own destination when it fails gracefully, but not when it
// is SIGKILLed — and a managed daemon's clone is killed by exactly the events
// this queue exists to survive: an OOM kill, or the workspace being suspended
// mid-clone. Before the atomic clone, the half-written checkout stayed at the
// project path, and the idempotency guard then saw its .git and reported the
// redelivered clone as SUCCESS: a broken project that every surface treated
// as installed. A stub git stands in for the killed one so the kill lands at a
// deterministic point.
func TestHandleGitClone_KilledCloneLeavesNoPartialCheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub git is a POSIX shell script")
	}
	setGitCloneTestEnv(t)
	bin := t.TempDir()
	stub := "#!/bin/sh\n" +
		"for a; do dest=$a; done\n" +
		"mkdir -p \"$dest/.git/objects\"\n" +
		"echo 'ref: refs/heads/main' > \"$dest/.git/HEAD\"\n" +
		"kill -9 $$\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	payload, err := json.Marshal(gitCloneRequest{Repo: "https://example.invalid/acme/widgets.git", Branch: "main", Path: dest})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := handleGitClone(context.Background(), payload); err == nil {
		t.Fatal("a killed clone must report failure")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("a killed clone left a partial checkout at the project path (stat err = %v)", err)
	}
	assertNoCloneStaging(t, parent)

	// JetStream redelivers the command. It must run the clone again, not
	// report the partial checkout as done.
	if out, err := handleGitClone(context.Background(), payload); err == nil {
		t.Fatalf("redelivery after a killed clone reported success: %s", out)
	}
}

// A clone that fails gracefully must also leave nothing at the project path.
// (git removes a destination it created itself on this kind of failure, so
// this guards the atomic path's own cleanup rather than a git behavior.)
func TestHandleGitClone_FailedCloneLeavesNoDirectory(t *testing.T) {
	setGitCloneTestEnv(t)
	origin := newTestOriginRepo(t)
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")

	payload, err := json.Marshal(gitCloneRequest{Repo: origin, Branch: "no-such-branch", Path: dest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleGitClone(context.Background(), payload); err == nil {
		t.Fatal("expected the clone of a missing branch to fail")
	}

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("a failed clone left something at the project path (stat err = %v)", err)
	}
	assertNoCloneStaging(t, parent)
}

// The target may already exist as an EMPTY directory — older code pre-created
// project directories on daemon connect. That directory is not the user's
// data and must not block the clone. `git clone` accepts an empty destination
// natively; the atomic path renames onto it instead, which fails on an
// existing directory unless it is cleared first — this pins that it is.
func TestHandleGitClone_EmptyTargetIsAdopted(t *testing.T) {
	setGitCloneTestEnv(t)
	origin := newTestOriginRepo(t)
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(gitCloneRequest{Repo: origin, Branch: "main", Path: dest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleGitClone(context.Background(), payload); err != nil {
		t.Fatalf("clone into an empty existing directory must succeed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("expected the checkout at the project path: %v", err)
	}
	assertNoCloneStaging(t, parent)
}

// A clone killed mid-way (the daemon OOM-killed or suspended) cannot run its
// cleanup, so its staging directory is orphaned. The next clone of the same
// target sweeps it rather than letting them accumulate — and lands the
// checkout at the real path, never at the stale staging one.
func TestHandleGitClone_SweepsStagingLeftByAKilledClone(t *testing.T) {
	setGitCloneTestEnv(t)
	origin := newTestOriginRepo(t)
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	orphan := filepath.Join(parent, cloneStagingPrefix+"dest-123456")
	if err := os.MkdirAll(filepath.Join(orphan, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A different target's staging directory must be left alone: that clone
	// may be running right now.
	other := filepath.Join(parent, cloneStagingPrefix+"other-123456")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(gitCloneRequest{Repo: origin, Branch: "main", Path: dest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleGitClone(context.Background(), payload); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphaned staging directory for this target was not swept (stat err = %v)", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another target's staging directory must be left alone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("expected the checkout at the project path: %v", err)
	}
}

// assertNoCloneStaging fails if a clone left its hidden staging directory
// behind in parent.
func assertNoCloneStaging(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), cloneStagingPrefix) {
			t.Fatalf("clone left its staging directory behind: %s", e.Name())
		}
	}
}
