package devstack

import (
	"fmt"
	"os"
	"path/filepath"
)

// lockRel is the global port-block lock. It guards the block registry's
// read-modify-write because claiming a block is one atomic operation from
// the user's view ("claim my stack's port offset"): the concurrent
// first-`up` of two worktrees must serialize so they get distinct blocks.
const lockRel = ".forge/blocks.lock"

// withLock runs fn while holding the exclusive, cross-process block lock.
// The lock is advisory (flock(2)) and auto-releases when the holding
// process exits or the fd closes — so a crashed `forge env up` never leaves a
// stale lock the next run has to time out on, unlike an O_EXCL marker file.
// Blocking (LOCK_EX without LOCK_NB): a second worktree's `up` waits for
// the first to finish its claim, then proceeds — exactly the serialization
// the block assignment needs.
// The lock lives beside the registry it guards, at the REPO ANCHOR (the
// primary checkout) rather than under projectDir. A per-worktree lock file
// serializes nothing: two worktrees claiming their first block would each
// lock their OWN file and race the shared registry anyway. See repoAnchor.
func withLock(projectDir string, fn func() error) error {
	return WithFileLock(filepath.Join(RepoAnchor(projectDir), lockRel), fn)
}

// WithFileLock serializes a registry transaction across processes. Readers of
// atomically replaced registries need no lock; every read-modify-write does.
func WithFileLock(p string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create .forge dir: %w", err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open block lock %s: %w", p, err)
	}
	defer func() { _ = f.Close() }()

	if err := lockFD(f); err != nil {
		return fmt.Errorf("acquire block lock %s: %w", p, err)
	}
	defer func() { _ = unlockFD(f) }()

	return fn()
}
