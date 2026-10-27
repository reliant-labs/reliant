//go:build linux

package osutil

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Capture pidfds before TERM can reap/reparent the group leader. Each handle
// names the original process even after PID recycling. This never signals a
// numeric PID asynchronously, and never scans command names or other groups.
func captureGroupKiller(proc *os.Process) func() {
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
	return func() {
		// Signal the original group through its leader's pidfd where supported,
		// plus every captured member for kernels that lack group pidfd signals or
		// when the leader has already exited. Handles cannot target PID reuse.
		if leaderFD >= 0 {
			_ = unix.PidfdSendSignal(leaderFD, unix.SIGKILL, nil, unix.PIDFD_SIGNAL_PROCESS_GROUP)
		}
		for _, fd := range fds {
			_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		}
		_ = proc.Kill()
		for _, fd := range fds {
			// A signal is not an exit acknowledgment (e.g. uninterruptible I/O).
			// Hold command completion until pidfd reports exit. The run.cancel
			// RPC has its own deadline and reports unconfirmed if this stalls.
			for {
				_, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, -1)
				if err != unix.EINTR {
					break
				}
			}
			unix.Close(fd)
		}
	}
}
