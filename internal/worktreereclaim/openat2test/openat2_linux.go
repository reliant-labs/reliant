// Copyright (c) 2026 Reliant Labs

// Package openat2test lets the reclaim packages' tests skip on a host that
// lacks the syscalls their safe inspection is built on.
package openat2test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// Missing reports why the host lacks one of the two
// syscalls the reaper's safe inspection is built on, or nil (probe).
func Missing() error { return probe() }

// probe checks, once per test binary, the two syscalls the reaper's safe
// inspection is built on: openat2 (RESOLVE_* path resolution) and renameat2
// (RENAME_NOREPLACE). It reports why the host lacks one only when the kernel
// or a seccomp/sandbox profile answers ENOSYS; any other result is nil, so a
// real defect still fails the test that hits it.
var probe = sync.OnceValue(func() error {
	dir, err := os.MkdirTemp("", "worktreereclaim-openat2-probe-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	fd, err := unix.Openat2(unix.AT_FDCWD, dir, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if errors.Is(err, unix.ENOSYS) {
		return fmt.Errorf("openat2: %w", err)
	}
	if err == nil {
		_ = unix.Close(fd)
	}
	from := filepath.Join(dir, "from")
	if err := os.WriteFile(from, nil, 0o600); err != nil {
		return nil
	}
	if err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, filepath.Join(dir, "to"), unix.RENAME_NOREPLACE); errors.Is(err, unix.ENOSYS) {
		return fmt.Errorf("renameat2 RENAME_NOREPLACE: %w", err)
	}
	return nil
})

// Require skips a test that needs openat2 or renameat2 on a host whose kernel
// or sandbox does not implement them (some CI runners' seccomp profiles answer
// ENOSYS). The host is missing a resource, so the test skips rather than
// fails. The reaper itself fails closed there: every inspection errors and
// the worktree is held, never deleted.
func Require(t testing.TB) {
	t.Helper()
	if err := Missing(); err != nil {
		t.Skipf("host lacks openat2 (ENOSYS): %v; the reaper holds every worktree on this host, and this test needs the syscall", err)
	}
}
