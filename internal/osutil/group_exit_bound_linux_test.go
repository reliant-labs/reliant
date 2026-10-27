//go:build linux

package osutil

import (
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"testing"
	"time"
)

func TestCapturedProcessJoinTimeoutAndInvalidHandleAreUnconfirmed(t *testing.T) {
	calls := 0
	err := awaitCapturedProcessExit([]unix.PollFd{{Fd: 10}}, time.Second, func(fds []unix.PollFd, ms int) (int, error) {
		calls++
		require.Positive(t, ms)
		return 0, nil
	})
	require.ErrorContains(t, err, "deadline elapsed")
	require.Equal(t, 1, calls)
	err = awaitCapturedProcessExit([]unix.PollFd{{Fd: 10}}, time.Second, func(fds []unix.PollFd, ms int) (int, error) { fds[0].Revents = unix.POLLNVAL; return 1, nil })
	require.ErrorContains(t, err, "invalid process handle")
}

func TestCapturedProcessJoinWaitsForEveryHandle(t *testing.T) {
	calls := 0
	err := awaitCapturedProcessExit([]unix.PollFd{{Fd: 10}, {Fd: 11}}, time.Second, func(fds []unix.PollFd, ms int) (int, error) {
		calls++
		if calls == 1 {
			require.Len(t, fds, 2)
		} else {
			require.Len(t, fds, 1)
			require.Equal(t, int32(11), fds[0].Fd)
		}
		fds[0].Revents = unix.POLLIN
		return 1, nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}
