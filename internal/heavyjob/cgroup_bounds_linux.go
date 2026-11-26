// Copyright (c) 2026 Reliant Labs
//go:build linux

package heavyjob

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cgroupBounds lowers the cpu.weight and memory.high of the daemon's command
// cgroup C (cmd-<daemon pid>-<n>, internal/proctree) while a slotted command
// runs in it, and puts back what it found when the command has exited.
//
// On C itself, not on the capped cgroup cpuCap makes under it: cpu.weight
// only divides CPU between siblings, and the capped cgroup's one sibling is
// the caller cgroup holding this wrapper, which never wants any. C's siblings
// are every other command the daemon runs — the agents' shells, their dev
// servers, other slotted commands — which is the contention the weight is
// for:
//
//	<delegated>/self          the daemon
//	<delegated>/commands/     every command, together
//	    cmd-<pid>-<n>         an interactive command, cpu.weight 100
//	    cmd-<pid>-<m>         this command line, cpu.weight lowered here
//	        heavy-<pid>-caller
//	        heavy-<pid>-command   cpu.max = the share
//
// memory.high goes on C too: C holds the command and this wrapper, a few MiB,
// and so it binds the command whether or not cpuCap made a cgroup for it.
//
// So, unlike cpu.max, these are files of the cgroup the wrapper runs in. That
// is safe because they are only ever lowered: a value already at or below the
// bound (one the daemon's operator set, or a nested wrapper's) is left alone,
// and release restores only a value still as the wrapper wrote it. Only in a
// daemon command cgroup, for the reason cpuCap is: one that holds this
// command line and nothing else. Where a file is missing (no cpu or memory
// controller enabled for the command cgroups, cgroup v1, not Linux) or not
// writable, that bound is not set, and nothing says so on the command's
// output: a missing limiter is not the command's failure.
type cgroupBounds struct {
	// dir is C; "" when nothing was lowered.
	dir string
	// lowered is each file written, with the value found and the value it
	// read back after the write (the kernel rounds memory.high to a page).
	lowered []loweredFile
}

type loweredFile struct{ name, was, now string }

// lowerBounds lowers this process's command cgroup to b.
func lowerBounds(b bounds) *cgroupBounds {
	return lowerBoundsAt("/proc/self/cgroup", cgroupFS, b)
}

// lowerBoundsAt is lowerBounds reading this process's cgroup from selfCgroup
// and the cgroup tree under fsRoot (tests).
func lowerBoundsAt(selfCgroup, fsRoot string, b bounds) *cgroupBounds {
	c := &cgroupBounds{}
	if b.weight < 1 && b.memoryHigh == 0 {
		return c
	}
	rel, why := ownCgroup(selfCgroup)
	if why != "" || !isCommandCgroupName(filepath.Base(rel)) {
		return c
	}
	dir := filepath.Join(fsRoot, rel)
	unlock, err := lockCgroup(dir)
	if err != nil {
		return c
	}
	defer unlock()
	c.dir = dir
	if b.weight > 0 {
		c.lower("cpu.weight", uint64(b.weight))
	}
	if b.memoryHigh > 0 {
		c.lower("memory.high", b.memoryHigh)
	}
	return c
}

// lower writes want to dir's interface file name when it reads higher ("max"
// is higher than anything), and records what it replaced.
func (c *cgroupBounds) lower(name string, want uint64) {
	path := filepath.Join(c.dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	was := strings.TrimSpace(string(data))
	if cur, ok := cgroupValue(was); !ok || cur <= want {
		return
	}
	if err := writeCgroupFile(c.dir, name, strconv.FormatUint(want, 10)); err != nil {
		return
	}
	now, err := os.ReadFile(path)
	if err != nil {
		return
	}
	c.lowered = append(c.lowered, loweredFile{name: name, was: was, now: strings.TrimSpace(string(now))})
}

// cgroupValue parses a single-value cgroup interface file: a number, or
// "max" for no limit.
func cgroupValue(s string) (uint64, bool) {
	if s == "max" {
		return math.MaxUint64, true
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// release puts back each value lower replaced, unless something else has
// changed it since. Best effort, and harmless twice: C is the daemon's, and
// removed with the command.
func (c *cgroupBounds) release() {
	if c.dir == "" || len(c.lowered) == 0 {
		return
	}
	if unlock, err := lockCgroup(c.dir); err == nil {
		defer unlock()
	}
	for _, f := range c.lowered {
		data, err := os.ReadFile(filepath.Join(c.dir, f.name))
		if err != nil || strings.TrimSpace(string(data)) != f.now {
			continue
		}
		_ = writeCgroupFile(c.dir, f.name, f.was)
	}
	c.lowered = nil
}
