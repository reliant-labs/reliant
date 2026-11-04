//go:build linux

package worktreereclaim

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// activityOpenRegular rejects symlinks in every path component atomically in
// the kernel. NONBLOCK prevents a swapped FIFO from blocking before fstat;
// fstat rejects every nonregular target before any bytes can be read. An old
// kernel without openat2 is unsupported and holds, never falls back to Open.
func activityOpenRegular(path string) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, abs, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("activity read target is not a regular file")
	}
	return f, nil
}
