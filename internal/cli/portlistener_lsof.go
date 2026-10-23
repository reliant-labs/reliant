//go:build !windows && !linux

package cli

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// portListenerPID returns the pid LISTENing on TCP <port>, or 0 when none is
// found / the lookup tool is unavailable. This is the macOS/BSD path, where
// lsof ships with the OS; Linux reads the kernel's socket tables directly
// (portlistener_linux.go) and Windows asks the TCP table API
// (procgroup_windows.go), because neither can count on lsof being installed.
// A 0 return classifies the holder as unidentifiable → foreign, so a missing
// lsof degrades to the safe "never reclaim an unknown process" behaviour
// rather than a misfire.
func portListenerPID(port int) int {
	if port <= 0 {
		return 0
	}
	out, err := exec.CommandContext(context.Background(),
		"lsof", "-nP", "-tiTCP:"+strconv.Itoa(port), "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	// lsof -t prints one pid per line; take the first.
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if pid, e := strconv.Atoi(line); e == nil {
				return pid
			}
		}
	}
	return 0
}
