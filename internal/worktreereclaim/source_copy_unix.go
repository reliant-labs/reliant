// Copyright (c) 2025 Reliant Labs
//go:build !windows

package worktreereclaim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// openSourceCopy opens the regular file at rel in the source checkout one
// component at a time from the checkout's root, refusing a symlink at every
// component below it (O_NOFOLLOW). The descriptor it returns is therefore the
// file at that very path inside the source: a symlinked directory there cannot
// redirect it out of the source, or into a checkout being removed, between a
// check and the open, because no check is separate from the open.
func openSourceCopy(source, rel string) (*os.File, error) {
	parts := splitSegments(rel)
	if len(parts) == 0 {
		return nil, errors.New("empty source path")
	}
	dir, err := unix.Open(source, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if dir >= 0 {
			_ = unix.Close(dir)
		}
	}()
	for _, name := range parts[:len(parts)-1] {
		if name == "." || name == ".." {
			return nil, fmt.Errorf("source path component %q", name)
		}
		fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		_ = unix.Close(dir)
		dir = fd
	}
	name := parts[len(parts)-1]
	if name == "." || name == ".." {
		return nil, fmt.Errorf("source path component %q", name)
	}
	// Only a regular file is opened: opening a device or a FIFO can block or
	// act. The open is then checked to be the file that was stat'ed.
	var st unix.Stat_t
	if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("source copy is not a regular file")
	}
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || opened.Dev != st.Dev || opened.Ino != st.Ino {
		_ = unix.Close(fd)
		return nil, errors.New("source copy replaced while opening")
	}
	return os.NewFile(uintptr(fd), filepath.Join(source, filepath.FromSlash(rel))), nil
}
