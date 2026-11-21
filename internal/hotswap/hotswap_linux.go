// Copyright (c) 2026 Reliant Labs

//go:build linux

package hotswap

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Supported reports whether this platform can hand commands across an exec.
const Supported = true

// stateCarrier puts the handoff in an anonymous memory file: nothing on disk
// to leave behind or to be read by another process.
func stateCarrier(state []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("reliant-hotswap", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("hotswap: memfd: %w", err)
	}
	f := os.NewFile(uintptr(fd), "reliant-hotswap")
	if _, err := f.Write(state); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("hotswap: write the handoff: %w", err)
	}
	return f, nil
}

func readState(f *os.File) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, 64<<20))
}

// setInheritable clears (or sets back) close-on-exec on files.
func setInheritable(files []*os.File, inherit bool) error {
	for _, f := range files {
		flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFD, 0)
		if err != nil {
			return fmt.Errorf("hotswap: F_GETFD %s: %w", f.Name(), err)
		}
		if inherit {
			flags &^= unix.FD_CLOEXEC
		} else {
			flags |= unix.FD_CLOEXEC
		}
		if _, err := unix.FcntlInt(f.Fd(), unix.F_SETFD, flags); err != nil {
			return fmt.Errorf("hotswap: F_SETFD %s: %w", f.Name(), err)
		}
	}
	return nil
}

func execve(path string, argv, env []string) error {
	return syscall.Exec(path, argv, env)
}

// adopt wraps an inherited descriptor, close-on-exec again so no command the
// new image starts inherits it.
func adopt(fd int, name string) *os.File {
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), name)
}
