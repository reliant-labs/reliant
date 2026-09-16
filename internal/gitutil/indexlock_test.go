// Copyright (c) 2025 Reliant Labs
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

// These tests shell out to real git. A fake would be testing our model of
// git's locking rather than git's locking, and the entire question here is
// what git actually does with index.lock.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// initRepo makes a repository with one commit and an identity, so commits work
// on a machine with no global git config.
func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "seed.txt")
	run(t, dir, "commit", "-qm", "base")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// ageLock backdates a lock past strandedLockMinAge. The age floor is a
// secondary guard, so tests that are about LIVENESS must not be gated on
// waiting out a wall-clock timer.
func ageLock(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-2 * strandedLockMinAge)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func lockPath(t *testing.T, repo string) string {
	t.Helper()
	gitDir, err := resolveGitDir(repo)
	if err != nil {
		t.Fatalf("resolveGitDir: %v", err)
	}
	return filepath.Join(gitDir, IndexLockName)
}

// TestEnsureIndexWritable_RecoversStrandedLock is the cure half: a lock left
// by a process that no longer exists must be cleared, and the next write must
// succeed. Without recovery this repository is permanently unwritable.
func TestEnsureIndexWritable_RecoversStrandedLock(t *testing.T) {
	repo := initRepo(t)
	lock := lockPath(t, repo)

	// A stranded lock: the file exists, nothing holds it open, nothing is
	// running. This is exactly the on-disk state a SIGKILLed git leaves.
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ageLock(t, lock)

	// Confirm the repository really is broken first, so the test proves a
	// recovery rather than asserting against a state that was already fine.
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pre := exec.Command("git", "add", "new.txt")
	pre.Dir = repo
	if out, err := pre.CombinedOutput(); err == nil {
		t.Fatalf("git add succeeded with a stranded lock in place; the test premise is wrong\n%s", out)
	} else if !strings.Contains(string(out), IndexLockName) {
		t.Fatalf("git add failed for an unexpected reason: %v\n%s", err, out)
	}

	EnsureIndexWritable(context.Background(), repo)

	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("stranded lock still present after EnsureIndexWritable (stat err = %v)", err)
	}

	out, err := RunIndexCommand(context.Background(), repo, "add", "new.txt")
	if err != nil {
		t.Fatalf("git add after recovery: %v\n%s", err, out)
	}
}

// TestEnsureIndexWritable_LeavesLiveLockAlone is THE dangerous direction, and
// it is pinned with a real concurrent git rather than a simulation.
//
// A slow clean filter makes `git add` hold index.lock for seconds, which is
// legitimate: a big repository or a slow pre-commit hook does the same. If
// recovery removed that lock, the running git would fail with "unable to write
// new index file" and lose its work — verified that this is the actual
// consequence.
func TestEnsureIndexWritable_LeavesLiveLockAlone(t *testing.T) {
	repo := initRepo(t)

	// A clean filter that sleeps: git holds index.lock for its duration.
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.slow filter=slow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "config", "filter.slow.clean", "sleep 4; cat")
	if err := os.WriteFile(filepath.Join(repo, "big.slow"), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	addCmd := exec.Command("git", "add", "big.slow")
	addCmd.Dir = repo
	if err := addCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = addCmd.Wait() })

	lock := lockPath(t, repo)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Skip("live git never produced an observable index.lock; cannot exercise the live-lock case here")
	}

	// Backdate it so the AGE guard cannot be what saves us. Only the liveness
	// probe stands between this lock and deletion — which is the property
	// under test, since age is not a safe test on its own.
	ageLock(t, lock)

	EnsureIndexWritable(context.Background(), repo)

	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("LIVE index.lock was removed (stat err = %v) — this corrupts the concurrent write", err)
	}

	// And the concurrent git must still succeed.
	if err := addCmd.Wait(); err != nil {
		t.Fatalf("concurrent git add failed: %v — its lock was interfered with", err)
	}
	status := run(t, repo, "status", "--porcelain")
	if !strings.Contains(status, "big.slow") {
		t.Fatalf("concurrent git add did not stage its file; status = %q", status)
	}
}

// TestClearStrandedIndexLock_HonoursGuards is the table of edge cases,
// including what the probe cannot detect.
func TestClearStrandedIndexLock_HonoursGuards(t *testing.T) {
	requireGit(t)

	tests := []struct {
		name       string
		setup      func(t *testing.T, lock string) (holdOpen *os.File)
		wantRemove bool
		why        string
	}{
		{
			name: "stranded lock, old, unheld",
			setup: func(t *testing.T, lock string) *os.File {
				mustWrite(t, lock)
				ageLock(t, lock)
				return nil
			},
			wantRemove: true,
			why:        "no live holder and past the age floor: the recoverable case",
		},
		{
			name: "fresh lock is never touched",
			setup: func(t *testing.T, lock string) *os.File {
				mustWrite(t, lock)
				return nil
			},
			wantRemove: false,
			why:        "a lock younger than the floor is overwhelmingly a live operation",
		},
		{
			name: "held open by a live process",
			setup: func(t *testing.T, lock string) *os.File {
				mustWrite(t, lock)
				ageLock(t, lock)
				f, err := os.OpenFile(lock, os.O_RDWR, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				return f
			},
			wantRemove: false,
			why:        "an open descriptor is the kernel-backed proof that the holder is alive",
		},
		{
			name: "no lock file at all",
			setup: func(t *testing.T, lock string) *os.File {
				return nil
			},
			wantRemove: false,
			why:        "nothing to do",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), IndexLockName)
			if f := tc.setup(t, lock); f != nil {
				defer f.Close()
			}
			existedBefore := fileExists(lock)

			clearStrandedIndexLock(context.Background(), lock)

			gone := !fileExists(lock)
			if tc.wantRemove && !gone {
				t.Fatalf("lock was kept but should have been removed (%s)", tc.why)
			}
			if !tc.wantRemove && existedBefore && gone {
				t.Fatalf("lock was REMOVED but should have been kept (%s)", tc.why)
			}
		})
	}
}

// TestResolveGitDir_LinkedWorktree pins that recovery targets the right
// directory in a linked worktree, where the index lives under
// .git/worktrees/<name>/ rather than in the main repository's .git/. Getting
// this wrong makes recovery silently inert in the setup this product creates
// most.
func TestResolveGitDir_LinkedWorktree(t *testing.T) {
	main := initRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")

	cmd := exec.Command("git", "worktree", "add", "-q", linked, "-b", "feature")
	cmd.Dir = main
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git worktree add failed: %v\n%s", err, out)
	}

	gitDir, err := resolveGitDir(linked)
	if err != nil {
		t.Fatalf("resolveGitDir(linked): %v", err)
	}
	if !strings.Contains(filepath.ToSlash(gitDir), "worktrees/") {
		t.Fatalf("resolveGitDir(linked) = %q, want the per-worktree git dir under .git/worktrees/", gitDir)
	}
	// The resolved dir is where git actually keeps this worktree's index.
	if _, err := os.Stat(filepath.Join(gitDir, "index")); err != nil {
		t.Fatalf("resolved git dir %q has no index file: %v", gitDir, err)
	}
}

func TestResolveGitDir_FromSubdirectory(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gitDir, err := resolveGitDir(sub)
	if err != nil {
		t.Fatalf("resolveGitDir: %v", err)
	}
	if filepath.Base(gitDir) != ".git" {
		t.Fatalf("resolveGitDir(subdir) = %q, want the repository's .git", gitDir)
	}
}

func TestResolveGitDir_NotARepository(t *testing.T) {
	if _, err := resolveGitDir(t.TempDir()); err == nil {
		t.Fatal("resolveGitDir on a non-repository returned nil error")
	}
}

// TestAnnotateIndexLockError: the user must learn what to do, not just that
// something is locked.
func TestAnnotateIndexLockError(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantAdvice  bool
		description string
	}{
		{
			name:        "index lock failure gets remediation",
			output:      "fatal: Unable to create '/repo/.git/index.lock': File exists.",
			wantAdvice:  true,
			description: "git blames a running process that usually is not running",
		},
		{
			name:        "unrelated failure is untouched",
			output:      "fatal: pathspec 'nope' did not match any files",
			wantAdvice:  false,
			description: "advice about locks would be noise here",
		},
		{
			name:        "empty output",
			output:      "",
			wantAdvice:  false,
			description: "nothing to annotate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnnotateIndexLockError(tc.output)
			hasAdvice := strings.Contains(got, IndexLockAdvice)
			if hasAdvice != tc.wantAdvice {
				t.Fatalf("advice present = %v, want %v (%s)\ngot: %q", hasAdvice, tc.wantAdvice, tc.description, got)
			}
			if !strings.Contains(got, tc.output) {
				t.Fatalf("annotation dropped the original git output: %q", got)
			}
		})
	}
}

func TestAnnotateIndexLockError_NotDuplicated(t *testing.T) {
	once := AnnotateIndexLockError("fatal: Unable to create '/r/.git/index.lock': File exists.")
	twice := AnnotateIndexLockError(once)
	if strings.Count(twice, IndexLockAdvice) != 1 {
		t.Fatalf("advice appended twice:\n%s", twice)
	}
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
