//go:build !windows

package debug

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// dlvPIDsForAddr returns PIDs of dlv processes whose command line contains
// --listen=<addr>. Best-effort via `ps`; returns nil when ps is unavailable.
func dlvPIDsForAddr(ctx context.Context, addr string) []int {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	listen := "--listen=" + addr
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		if pid, ok := dlvPSLineMatches(line, listen); ok {
			pids = append(pids, pid)
		}
	}
	return pids
}

// dlvPSLineMatches parses a `ps -o pid=,command=` line and reports its pid
// when the command is a dlv server carrying the listen flag.
func dlvPSLineMatches(line, listen string) (int, bool) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.Contains(line, listen) || !strings.Contains(line, "dlv") {
		return 0, false
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	return pid, err == nil
}

// isDlvForAddr reports whether pid is currently a dlv server listening on
// addr. Unreadable or mismatched means false, so a recycled pid is never killed.
func isDlvForAddr(ctx context.Context, pid int, addr string) bool {
	if pid <= 0 || addr == "" {
		return false
	}
	out, err := exec.CommandContext(ctx, "ps", "-o", "pid=,command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	got, ok := dlvPSLineMatches(string(out), "--listen="+addr)
	return ok && got == pid
}
