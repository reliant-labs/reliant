//go:build linux

package cli

import (
	"os"
	"strconv"
	"strings"
)

// readProcEnviron returns pid's environment as KEY=VALUE strings on Linux
// by reading /proc/<pid>/environ (NUL-separated). ok is false when the file
// is unreadable (dead pid, or a process owned by another uid whose environ
// the kernel withholds) — an unreadable holder is treated as NOT-forge-owned
// so an unidentifiable process is never reclaimed.
func readProcEnviron(pid int) ([]string, bool) {
	if pid <= 0 {
		return nil, false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return nil, false
	}
	var out []string
	for _, kv := range strings.Split(string(data), "\x00") {
		if kv != "" {
			out = append(out, kv)
		}
	}
	return out, true
}

// readProcArgv returns pid's argv on Linux by reading /proc/<pid>/cmdline
// (NUL-separated). Used by `forge env status` duplicate attribution to tell two
// processes carrying the SAME ownership marker apart by what they are actually
// running (`reliant server worker` vs `reliant server api`).
//
// ok is false when the file is unreadable (dead pid, another uid's process) or
// when it is empty — a kernel thread has an empty cmdline, and so does a
// process caught mid-exec. Callers must surface that as "could not determine
// attribution" rather than collapsing it into a guess.
func readProcArgv(pid int) ([]string, bool) {
	if pid <= 0 {
		return nil, false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, false
	}
	// cmdline is NUL-SEPARATED and NUL-TERMINATED, so the trailing NUL yields
	// an empty final field that is not an argument. Every other field is
	// positional and kept verbatim, empty ones included.
	raw := strings.TrimSuffix(string(data), "\x00")
	if raw == "" {
		return nil, false
	}
	return strings.Split(raw, "\x00"), true
}

// procExecPath returns the path of the binary pid is executing on Linux, from
// the /proc/<pid>/exe symlink. Used by `forge env status` build-freshness to
// show which binary a host service's server process is running (e.g. air's
// `tmp/main`). When the on-disk binary has been replaced under a running
// process (an air rebuild), the kernel appends " (deleted)" to the link
// target — a load-bearing signal that the process is running a stale build,
// so it is preserved in the returned path rather than stripped. ok is false
// when the link is unreadable (dead pid, or another uid's process).
func procExecPath(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil || target == "" {
		return "", false
	}
	return target, true
}
