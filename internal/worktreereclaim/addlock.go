// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// The add lock coordinates reliant's `git worktree add` with the repair that
// prunes registrations. git takes no lock of its own around an add: it
// creates the registration (locked "initializing"), then the checkout's
// directory, then fills both in. A prune that decided a registration was dead
// and then removed it could otherwise interleave with an add making that same
// checkout again, and delete the metadata of a live checkout, freeing its
// branch for another add.
//
// So each add holds the repository's add lock SHARED for as long as git runs
// (adds do not exclude one another), and a prune holds it EXCLUSIVE from its
// final "is the checkout gone" check through the removal. A prune never waits
// for it: an add in flight means the registration is left for the next
// repair. The lock is an advisory file lock in the repository's common dir,
// so it covers every daemon process on the machine and dies with its holder.

// addLockName is the add lock's file in a repository's common dir. It does not
// end in ".lock": git and this package's repair treat those as git's own.
const addLockName = "reliant-worktree-add.flock"

// LockWorktreeAdd takes repoPath's add lock shared, waiting while a prune
// holds it, until ctx is done. The caller holds it across `git worktree add`
// and calls the returned release once git has exited.
func LockWorktreeAdd(ctx context.Context, repoPath string) (release func(), err error) {
	common := repoCommonDir(ctx, repoPath)
	if common == "" {
		return nil, fmt.Errorf("%s: cannot find its git directory to lock worktree creation", repoPath)
	}
	release, err = lockFileShared(ctx, filepath.Join(common, addLockName))
	if err != nil {
		return nil, fmt.Errorf("waiting for the worktree add lock of %s: %w", common, err)
	}
	return release, nil
}

// tryLockPrune takes commonDir's add lock exclusive without waiting. It
// reports false, with no error, when an add holds it.
func tryLockPrune(commonDir string) (release func(), ok bool, err error) {
	f, err := os.OpenFile(filepath.Join(commonDir, addLockName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	ok, err = tryFlock(f, true)
	if err != nil || !ok {
		f.Close()
		return nil, false, err
	}
	return func() {
		unflock(f)
		f.Close()
	}, true, nil
}

// lockFileShared takes a shared lock on path, polling until it is free of an
// exclusive holder or ctx is done.
func lockFileShared(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := pollLock(ctx, func() (bool, error) { return tryFlock(f, false) }); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		unflock(f)
		f.Close()
	}, nil
}
