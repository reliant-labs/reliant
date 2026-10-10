// Copyright (c) 2025 Reliant Labs
//go:build !windows

package gitutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/osutil"
)

// slowStagingRepo builds a repository where `git add` is guaranteed to be
// holding index.lock when we cancel it. A clean filter that sleeps is the
// reliable way to hit that window: without it, staging finishes in
// milliseconds and the cancellation lands either before the lock exists or
// after it is gone, so the test would pass for the wrong reason.
//
// The filter touches started before it sleeps; see waitForStagingFilter.
func slowStagingRepo(t *testing.T) (repo, target, started string) {
	t.Helper()
	repo = initRepo(t)
	started = filepath.Join(t.TempDir(), "filter-started")

	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.slow filter=slow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "config", "filter.slow.clean", fmt.Sprintf("touch '%s'; sleep 5; cat", started))

	target = "payload.slow"
	if err := os.WriteFile(filepath.Join(repo, target), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, target, started
}

// waitForStagingFilter blocks until git is inside the slow clean filter with
// index.lock held.
//
// The lock file appearing is not enough to cancel on. git creates the file
// (tempfile.c: open) and only THEN registers it, together with the signal
// handler that removes it (activate_tempfile). A SIGTERM between the two finds
// nothing to clean up, and the lock strands whichever way git is stopped. A
// loaded CI runner preempted git in exactly that window. git add takes the
// lock before it filters anything (builtin/add.c), so the filter having
// started proves both steps are done.
func waitForStagingFilter(t *testing.T, repo, started string, within time.Duration) bool {
	t.Helper()
	lock := lockPath(t, repo)
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(started); err == nil {
			_, err := os.Stat(lock)
			return err == nil
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
	if testing.Short() {
		t.Skip("builds a slow-to-stage repo and cancels real git index writes mid-flight (~3s); runs in make test")
	}
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
			repo, target, started := slowStagingRepo(t)
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

			if !waitForStagingFilter(t, repo, started, 5*time.Second) {
				t.Skip("git never reached the clean filter holding index.lock; cannot exercise cancellation mid-write")
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
// entry point the call sites use: a lock stranded by an earlier death must not
// require any user intervention. The holder probe is scripted to "not held",
// for the reason given at the top of indexlock_test.go.
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

	out, err := runIndexCommand(context.Background(), repo, probeSays(osutil.FileHoldNotHeld), "add", "recovered.txt")
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
