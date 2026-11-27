//go:build linux

package worktreereclaim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// HostLacksSafeRemoval reports which syscall the mount-safe inspection and
// removal are built on that this host's kernel or seccomp/sandbox profile
// does not implement: openat2 (RESOLVE_* path resolution) or renameat2
// (RENAME_NOREPLACE). It is nil when neither answers ENOSYS; any other
// result is left to the caller that hits it. On such a host the reaper fails
// closed: every inspection errors and every worktree is held, never deleted.
// Tests that expect a removal skip on it. The probe runs once per process.
func HostLacksSafeRemoval() error { return safeRemovalProbe() }

var safeRemovalProbe = sync.OnceValue(func() error {
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
		unix.Close(fd)
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
