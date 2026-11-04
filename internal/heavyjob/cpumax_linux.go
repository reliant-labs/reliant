// Copyright (c) 2026 Reliant Labs
//go:build linux

package heavyjob

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// cpuPeriod is the cpu.max period, cgroup v2's default: 100ms.
const cpuPeriod = 100_000

// cgroupFS is where cgroup v2 is mounted.
const cgroupFS = "/sys/fs/cgroup"

// commandCgroupPrefix names the cgroup the daemon starts each command in
// (internal/proctree's cmdPrefix): cmd-<daemon pid>-<n>.
const commandCgroupPrefix = "cmd-"

// limitCPU caps the cgroup this wrapper runs in at share CPUs (cpu.max), so
// a slotted command cannot use more than its share however many threads it
// starts: GOMAXPROCS bounds the Go processes that honour it, and this bounds
// everything else — cc1 and ld under cgo, a node build, a command that set its
// own GOMAXPROCS. Past the slots' total, work queues for a slot instead of
// piling onto the run queue, where it timed out and was killed (2026-11-03:
// load 150–320 on 31 CPUs).
//
// Only the daemon's own command cgroup (cmd-*), which holds this command and
// nothing else: a wrapper started anywhere else must not throttle whatever
// shares its cgroup. Where the cpu controller is not enabled for it (a
// daemon that could not enable it, no cgroup v2) there is no cpu.max file,
// and the share stays GOMAXPROCS alone. It returns why it set nothing, ""
// when it set the cap.
func limitCPU(share int) string { return limitCPUAt("/proc/self/cgroup", cgroupFS, share) }

// limitCPUAt is limitCPU reading this process's cgroup from selfCgroup and
// writing under fsRoot (tests).
func limitCPUAt(selfCgroup, fsRoot string, share int) string {
	if share < 1 {
		return "no share"
	}
	data, err := os.ReadFile(selfCgroup)
	if err != nil {
		return "no /proc/self/cgroup"
	}
	own := ""
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			own = p
		}
	}
	// The kernel writes an absolute, clean path; anything else (a relative
	// path, a ".." that would leave fsRoot) is not a cgroup to write to.
	rel, ok := strings.CutPrefix(own, "/")
	if !ok || !filepath.IsLocal(rel) || filepath.Clean(own) != own {
		return "not in a daemon command cgroup"
	}
	if !isCommandCgroupName(filepath.Base(own)) {
		return "not in a daemon command cgroup"
	}
	f := filepath.Join(fsRoot, rel, "cpu.max")
	// Never created: a cgroup without the cpu controller has no cpu.max, and
	// that means no cap, not a file to make.
	fh, err := os.OpenFile(f, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err.Error()
	}
	_, werr := fmt.Fprintf(fh, "%d %d", share*cpuPeriod, cpuPeriod)
	if cerr := fh.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr.Error()
	}
	return ""
}

// isCommandCgroupName reports whether name is internal/proctree's command
// cgroup name: cmd-<daemon pid>-<seq>.
func isCommandCgroupName(name string) bool {
	rest, ok := strings.CutPrefix(name, commandCgroupPrefix)
	if !ok {
		return false
	}
	pid, seq, ok := strings.Cut(rest, "-")
	return ok && allDigits(pid) && allDigits(seq)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
