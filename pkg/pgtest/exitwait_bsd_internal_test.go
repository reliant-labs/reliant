//go:build darwin || freebsd || netbsd || openbsd

package pgtest

import (
	"errors"

	"golang.org/x/sys/unix"
)

// waitExit blocks until pid, which need not be a child of this process, has
// exited: kqueue reports the exit itself (EVFILT_PROC/NOTE_EXIT), so no
// duration orders the caller after it. A pid already gone counts as exited.
func waitExit(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(kq) }()
	ev := unix.Kevent_t{}
	unix.SetKevent(&ev, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	ev.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		return err
	}
	out := make([]unix.Kevent_t, 1)
	for {
		_, err := unix.Kevent(kq, nil, out, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
