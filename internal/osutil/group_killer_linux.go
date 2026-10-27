//go:build linux

package osutil

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// awaitCapturedProcessExit bounds only the captured-handle join. It is NOT a
// proof that a snapshot contained every descendant; the caller reports that
// limitation separately. The injected poll keeps timeout/error tests hermetic.
func awaitCapturedProcessExit(fds []unix.PollFd, budget time.Duration, poll func([]unix.PollFd, int) (int, error)) error {
	deadline := time.Now().Add(budget)
	for len(fds) > 0 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("captured process exit unconfirmed: shutdown deadline elapsed")
		}
		ms := int((remaining + time.Millisecond - 1) / time.Millisecond)
		n, err := poll(fds, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("captured process exit unconfirmed: %w", err)
		}
		if n == 0 {
			return errors.New("captured process exit unconfirmed: shutdown deadline elapsed")
		}
		pending := fds[:0]
		for _, fd := range fds {
			if fd.Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
				return errors.New("captured process exit unconfirmed: invalid process handle")
			}
			if fd.Revents&unix.POLLIN == 0 {
				fd.Revents = 0
				pending = append(pending, fd)
			}
		}
		fds = pending
	}
	return nil
}

// Capture pidfds before TERM can reap/reparent the group leader. Each handle
// names the original process even after PID recycling. This never signals a
// numeric PID asynchronously, and never scans command names or other groups.
func captureGroupKiller(proc *os.Process) func() error {
	var fds []int
	leaderFD := -1
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			unix.Close(fd)
			continue
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			unix.Close(fd)
			continue
		}
		fields := strings.Fields(string(stat[end+1:]))
		if len(fields) < 3 || fields[2] != strconv.Itoa(proc.Pid) {
			unix.Close(fd)
			continue
		}
		fds = append(fds, fd)
		if pid == proc.Pid {
			leaderFD = fd
		}
	}
	return func() error {
		// Signal the original group through its leader's pidfd where supported,
		// plus every captured member for kernels that lack group pidfd signals or
		// when the leader has already exited. Handles cannot target PID reuse.
		var proofErr error
		if leaderFD >= 0 {
			if err := unix.PidfdSendSignal(leaderFD, unix.SIGKILL, nil, unix.PIDFD_SIGNAL_PROCESS_GROUP); err != nil {
				proofErr = fmt.Errorf("process-group escalation unconfirmed: %w", err)
			}
		} else {
			proofErr = errors.New("process-group escalation unconfirmed: original leader unavailable")
		}
		for _, fd := range fds {
			_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		}
		_ = proc.Kill()
		// One total deadline, not one timeout per member. An uninterruptible
		// child must yield uncertainty instead of hanging the daemon forever.
		pollfds := make([]unix.PollFd, 0, len(fds))
		for _, fd := range fds {
			pollfds = append(pollfds, unix.PollFd{Fd: int32(fd), Events: unix.POLLIN})
		}
		proofErr = errors.Join(proofErr, awaitCapturedProcessExit(pollfds, 3*time.Second, unix.Poll))
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
		// A one-time member snapshot cannot prove absence of a later fork.
		// Preserve best-effort cleanup but never advertise it as whole-tree exit.
		return errors.Join(proofErr, errors.New("complete process-tree exit unconfirmed: snapshot cannot fence new descendants"))
	}
}
