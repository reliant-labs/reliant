// Copyright (c) 2025 Reliant Labs
package gitutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/osutil"
)

// IndexLockName is the file whose mere existence is git's index lock. Git
// does not flock it — creating it O_EXCL is the claim, and removing it is the
// release — which is why ownership has to be established indirectly.
const IndexLockName = "index.lock"

// strandedLockMinAge is how old a lock must be before removal is even
// considered.
//
// This is NOT the test for staleness. Age cannot be that test: a `git commit`
// on a very large repository, or one running a slow clean filter or
// pre-commit hook, can legitimately hold the lock for minutes, and deleting
// it would corrupt that write. Measured: removing a live lock makes the
// running git fail with "fatal: unable to write new index file" and lose its
// work.
//
// It is a secondary floor layered UNDER the liveness probe, and it earns its
// place by covering the probe's blind spots rather than duplicating it — a
// lock held from another host over a network filesystem is invisible to a
// host-local probe, and this bounds how quickly such a lock could be removed.
//
// Thirty seconds because the two populations are far apart: a genuinely
// stranded lock is minutes to hours old (the case that motivated this was
// twelve hours), while a fresh lock is overwhelmingly a live operation. The
// cost of waiting is small and falls on the case prevention already handles —
// a lock stranded by OUR cancellation should no longer happen now that
// subprocesses get SIGTERM and remove their own locks.
const strandedLockMinAge = 30 * time.Second

// lockStabilityDelay separates the two liveness observations.
//
// There is a brief window in a normal git run where the lock file exists but
// no descriptor points at it — between git closing the lock and renaming it
// over the index. Sampling live `git add`, `git commit -a` and `git stash` at
// full speed, every apparently-unheld observation turned out to be that race
// (on re-check the file had already vanished or been replaced): across all
// runs, REAL unheld-with-same-inode observations numbered zero. Requiring the
// same inode to still be present and still unheld after this delay turns that
// window from a false "stale" verdict into a correctly-skipped one.
const lockStabilityDelay = 250 * time.Millisecond

// IndexLockAdvice is the remediation text attached to a lock failure we chose
// not to resolve automatically. A user told only "another git process seems to
// be running" — when typically none is — has no next step; this gives one.
const IndexLockAdvice = "If no git process is actually running, this lock was left behind by one that was killed. " +
	"Check with `git status` first, then remove it with `rm -f <git-dir>/index.lock` and retry."

// EnsureIndexWritable clears a stranded index.lock at dir when, and only when,
// it can be shown that no live process holds it.
//
// It is called before an index-writing git command so that a lock left by a
// process that died without cleanup — an OOM kill, `kill -9`, a power loss, or
// a cancellation from before this code existed — self-heals instead of
// permanently breaking every write in the repository.
//
// It is best-effort by construction and reports no error: a lock it declines
// to touch is left for the git command itself to fail on, with the real git
// error message plus IndexLockAdvice, which is strictly more informative than
// anything this function could say pre-emptively.
//
// # The safety argument
//
// The dangerous direction is deleting a LIVE lock, which corrupts a concurrent
// write. Every condition below is necessary, and they are ordered cheapest
// first:
//
//  1. The lock file exists (otherwise there is nothing to do).
//  2. It is at least strandedLockMinAge old.
//  3. No live process holds it open — the kernel-backed signal, and the only
//     one that cannot be made stale by a process dying or confused by pid
//     reuse. Unknown is NOT unheld: an unanswerable probe leaves the lock.
//  4. Still true after lockStabilityDelay, with the SAME inode, which rules
//     out the close-then-rename window of a healthy git run.
//  5. The inode is re-verified immediately before unlinking, so a lock created
//     by a new process in the meantime is not removed in place of the one that
//     was judged.
//
// What remains uncovered, stated rather than papered over: a lock held from a
// different machine over a network filesystem is invisible to a host-local
// probe. Conditions 2 and 4 narrow that window but do not close it. On
// Windows the probe always answers Unknown, so nothing is ever removed there.
func EnsureIndexWritable(ctx context.Context, dir string) {
	gitDir, err := resolveGitDir(dir)
	if err != nil || gitDir == "" {
		return
	}
	clearStrandedIndexLock(ctx, filepath.Join(gitDir, IndexLockName))
}

// clearStrandedIndexLock applies the conditions documented on
// EnsureIndexWritable to one concrete lock path.
func clearStrandedIndexLock(ctx context.Context, lockPath string) {
	info, err := os.Stat(lockPath)
	if err != nil {
		return // no lock, or unreadable — either way, not ours to clear
	}

	age := time.Since(info.ModTime())
	if age < strandedLockMinAge {
		return
	}

	if osutil.FileHolders(ctx, lockPath) != osutil.FileHoldNotHeld {
		return // held, or unknowable: leave it
	}

	firstIno, ok := fileIdentity(lockPath)
	if !ok {
		return
	}

	// Second observation: a lock in git's close-then-rename window will have
	// vanished or been replaced by now.
	select {
	case <-ctx.Done():
		return
	case <-time.After(lockStabilityDelay):
	}

	secondIno, ok := fileIdentity(lockPath)
	if !ok || secondIno != firstIno {
		return // replaced or removed under us: a live git, not a stranded lock
	}
	if osutil.FileHolders(ctx, lockPath) != osutil.FileHoldNotHeld {
		return
	}

	// Final inode check immediately before the unlink, so the file removed is
	// provably the same one judged stale rather than a successor created in
	// the interim.
	finalIno, ok := fileIdentity(lockPath)
	if !ok || finalIno != firstIno {
		return
	}

	if err := os.Remove(lockPath); err != nil {
		if !os.IsNotExist(err) {
			logging.Warn("Failed to remove stranded git index lock",
				"path", lockPath, "error", err)
		}
		return
	}

	logging.Info("Removed stranded git index lock left by a process that is no longer running",
		"path", lockPath, "age", age.Round(time.Second).String())
}

// resolveGitDir finds the directory holding the index for the working tree at
// dir, walking up to the repository root if dir is a subdirectory.
//
// A LINKED WORKTREE keeps its index in .git/worktrees/<name>/, not in the main
// repository's .git/ — verified directly: during a live `git add` in a linked
// worktree the lock appeared at
// <main>/.git/worktrees/<name>/index.lock and the main repository's
// .git/index.lock did not exist. Resolving to the wrong directory would make
// recovery silently inert in exactly the worktree-heavy setup this product
// creates, so the `gitdir:` pointer file is followed.
//
// This reads the filesystem rather than shelling out to `git rev-parse`,
// because it runs before every index write and a subprocess per call is real
// cost for a check that is usually a single stat.
func resolveGitDir(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	for {
		gitPath := filepath.Join(abs, ".git")
		info, err := os.Lstat(gitPath)
		switch {
		case err == nil && info.IsDir():
			return gitPath, nil
		case err == nil && info.Mode().IsRegular():
			// Linked worktree or submodule: a pointer file whose sole line is
			// "gitdir: <path>", absolute or relative to this directory.
			data, readErr := os.ReadFile(gitPath)
			if readErr != nil {
				return "", readErr
			}
			target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
			if target == "" {
				return "", fmt.Errorf("malformed .git pointer at %s", gitPath)
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(abs, target)
			}
			return filepath.Clean(target), nil
		}

		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("not a git repository: %s", dir)
		}
		abs = parent
	}
}

// AnnotateIndexLockError appends actionable remediation to git output that
// reports a locked index, and returns other output unchanged.
//
// Git's own message ("Another git process seems to be running in this
// repository...") names a cause that is usually false — in the case that
// motivated this work no git process existed and the lock was twelve hours
// old — and gives the user nothing to do. Since the recovery path above
// deliberately declines to remove anything it cannot prove is stranded, the
// user needs to be told what the remaining manual step is.
func AnnotateIndexLockError(output string) string {
	if !MentionsIndexLock(output) {
		return output
	}
	if strings.Contains(output, IndexLockAdvice) {
		return output
	}
	if strings.TrimSpace(output) == "" {
		return IndexLockAdvice
	}
	return strings.TrimRight(output, "\n") + "\n\n" + IndexLockAdvice
}

// MentionsIndexLock reports whether git output describes an index-lock
// failure. Matching is on the lock FILENAME, which appears in the "Unable to
// create '<path>/index.lock': File exists." line and is stable across git's
// localized and reworded variants of the surrounding prose.
func MentionsIndexLock(output string) bool {
	return strings.Contains(output, IndexLockName)
}
