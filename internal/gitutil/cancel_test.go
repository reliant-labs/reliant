// Copyright (c) 2025 Reliant Labs
//go:build !windows

package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// slowStagingRepo builds a repository where `git add` is guaranteed to be
// holding index.lock when we cancel it. A clean filter that sleeps is the
// reliable way to hit that window: without it, staging finishes in
// milliseconds and the cancellation lands either before the lock exists or
// after it is gone, so the test would pass for the wrong reason.
func slowStagingRepo(t *testing.T) (repo, target string) {
	t.Helper()
	repo = initRepo(t)

	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.slow filter=slow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "config", "filter.slow.clean", "sleep 5; cat")

	target = "payload.slow"
	if err := os.WriteFile(filepath.Join(repo, target), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, target
}

// waitForLock blocks until the repository's index.lock appears.
func waitForLock(t *testing.T, repo string, within time.Duration) bool {
	t.Helper()
	lock := lockPath(t, repo)
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lock); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestCancelledIndexWrite_DoesNotStrandLock is the prevention proof, and the
// two subtests are a matched pair on purpose.
//
//   - "default cancellation" runs git through plain exec.CommandContext, which
//     is what every call site in this repository did before this change. It
//     asserts the lock IS stranded. That subtest is the bug, reproduced.
//   - "graceful cancellation" runs the same git through the shared helper and
//     asserts the lock is gone and the repository is writable again.
//
// Keeping the pre-fix path in the test file is what makes this a proof rather
// than an assertion: if the fix regresses, the second subtest fails, and if
// the bug ever stops reproducing the first one fails and tells us the premise
// has changed.
func TestCancelledIndexWrite_DoesNotStrandLock(t *testing.T) {
	tests := []struct {
		name        string
		graceful    bool
		wantStrand  bool
		description string
	}{
		{
			name:        "default cancellation strands the lock",
			graceful:    false,
			wantStrand:  true,
			description: "os/exec SIGKILLs the child, so git's atexit handler never removes index.lock",
		},
		{
			name:        "graceful cancellation lets git clean up",
			graceful:    true,
			wantStrand:  false,
			description: "SIGTERM lets git remove its own lock before exiting",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, target := slowStagingRepo(t)
			lock := lockPath(t, repo)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var cmd *exec.Cmd
			var stop func()
			if tc.graceful {
				cmd, stop = IndexCommand(ctx, repo, "add", target)
				defer stop()
			} else {
				cmd = exec.CommandContext(ctx, "git", "add", target)
				cmd.Dir = repo
			}

			if err := cmd.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}

			if !waitForLock(t, repo, 5*time.Second) {
				t.Skip("git never produced an observable index.lock; cannot exercise cancellation mid-write")
			}

			cancel()
			_ = cmd.Wait()

			// Give git's signal handler a moment to run. The default path has
			// no handler to run, so this delay cannot mask the bug — it only
			// avoids racing the graceful path's cleanup.
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(lock); os.IsNotExist(err) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}

			_, statErr := os.Stat(lock)
			stranded := statErr == nil

			if tc.wantStrand && !stranded {
				t.Fatalf("expected the pre-fix path to strand index.lock (%s); it did not, so this test no longer reproduces the bug", tc.description)
			}
			if !tc.wantStrand && stranded {
				t.Fatalf("index.lock was stranded by a cancelled git (%s)", tc.description)
			}

			// For the fixed path, prove the repository is actually usable —
			// an absent lock is only meaningful if the next write succeeds.
			if !tc.wantStrand {
				if err := os.WriteFile(filepath.Join(repo, "after.txt"), []byte("after\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				out, err := RunIndexCommand(context.Background(), repo, "add", "after.txt")
				if err != nil {
					t.Fatalf("git add after a cancelled write: %v\n%s", err, out)
				}
			}
		})
	}
}

// TestRunIndexCommand_RecoversThenSucceeds exercises the cure through the
// public entry point the call sites use: a lock stranded by an earlier death
// must not require any user intervention.
func TestRunIndexCommand_RecoversThenSucceeds(t *testing.T) {
	repo := initRepo(t)
	lock := lockPath(t, repo)

	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ageLock(t, lock)

	if err := os.WriteFile(filepath.Join(repo, "recovered.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := RunIndexCommand(context.Background(), repo, "add", "recovered.txt")
	if err != nil {
		t.Fatalf("RunIndexCommand did not recover from a stranded lock: %v\n%s", err, out)
	}

	status := run(t, repo, "status", "--porcelain")
	if !strings.Contains(status, "recovered.txt") {
		t.Fatalf("file was not staged; status = %q", status)
	}
}

// TestRunIndexCommand_SucceedsNormally guards the boring case: the helper must
// not break ordinary, uncancelled git usage.
func TestRunIndexCommand_SucceedsNormally(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "plain.txt"), []byte("plain\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := RunIndexCommand(context.Background(), repo, "add", "plain.txt"); err != nil {
		t.Fatalf("RunIndexCommand: %v\n%s", err, out)
	}
	if out, err := RunIndexCommand(context.Background(), repo, "commit", "-qm", "added"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}

	if log := run(t, repo, "log", "--oneline"); !strings.Contains(log, "added") {
		t.Fatalf("commit did not land; log = %q", log)
	}
}

// TestRunIndexCommand_ReportsGitFailureUnchanged: a genuine git error must
// still reach the caller as a failure with git's own message.
func TestRunIndexCommand_ReportsGitFailureUnchanged(t *testing.T) {
	repo := initRepo(t)
	out, err := RunIndexCommand(context.Background(), repo, "add", "does-not-exist.txt")
	if err == nil {
		t.Fatalf("expected failure for a missing pathspec; got output %q", out)
	}
	if !strings.Contains(string(out), "did not match any files") {
		t.Fatalf("git's own error text was lost: %q", out)
	}
}
