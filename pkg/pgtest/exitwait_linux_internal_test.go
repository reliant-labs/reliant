//go:build linux

package pgtest

import (
	"errors"

	"golang.org/x/sys/unix"
)

// waitExit blocks until pid, which need not be a child of this process, has
// exited: its pidfd turns readable on the exit itself, so no duration orders
// the caller after it. A pid already gone counts as exited.
func waitExit(pid int) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for {
		_, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, -1)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
