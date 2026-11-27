//go:build linux

package worktreereclaim

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// HostLacksSafeRemoval reports failures probing the syscalls that mount-safe
// inspection and removal require: openat2 (RESOLVE_* path resolution) and
// renameat2 (RENAME_NOREPLACE). It is nil only when both probes succeed;
// setup failures also return an error, since support could not be established.
// When a required syscall is unavailable, the reaper fails closed: every
// inspection errors and every worktree is held, never deleted.
// Tests that expect a removal skip on probe errors. The probe runs once per process.
func HostLacksSafeRemoval() error { return safeRemovalProbe() }

var safeRemovalProbe = sync.OnceValue(func() error {
	dir, err := os.MkdirTemp("", "worktreereclaim-openat2-probe-")
	if err != nil {
		return fmt.Errorf("create safe-removal probe directory: %w", err)
	}
	defer os.RemoveAll(dir)
	fd, err := unix.Openat2(unix.AT_FDCWD, dir, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return fmt.Errorf("openat2: %w", err)
	}
	unix.Close(fd)
	from := filepath.Join(dir, "from")
	if err := os.WriteFile(from, nil, 0o600); err != nil {
		return fmt.Errorf("create safe-removal probe file: %w", err)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, filepath.Join(dir, "to"), unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("renameat2 RENAME_NOREPLACE: %w", err)
	}
	return nil
})
