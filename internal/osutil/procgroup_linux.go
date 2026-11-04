// Copyright (c) 2026 Reliant Labs
//go:build linux

package osutil

import (
	"os"
	"strconv"
	"strings"
)

// GroupMembers lists the live processes of process group pgid. Zombies are
// not live: a zombie runs nothing, and one whose reaper never comes (a
// container whose PID 1 does not reap) would otherwise look like a workload
// that survived SIGKILL forever. ok is false where the process table cannot
// be read.
func GroupMembers(pgid int) (members []GroupMember, ok bool) {
	if pgid <= 0 {
		return nil, true
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		state, group, start, ok := readProcStat(pid)
		if ok && group == pgid && state != 'Z' && state != 'X' {
			members = append(members, GroupMember{PID: pid, Start: start})
		}
	}
	return members, true
}

// GroupOwned reports whether any of members is still alive, the same process
// (same start time), and still in group pgid — proof that pgid is still the
// group they were recorded in.
func GroupOwned(pgid int, members []GroupMember) bool {
	for _, m := range members {
		state, group, start, ok := readProcStat(m.PID)
		if ok && group == pgid && start == m.Start && state != 'Z' && state != 'X' {
			return true
		}
	}
	return false
}

// readProcStat reads a process's state (field 3), process group (field 5) and
// start time (field 22) from /proc/<pid>/stat. The command name (field 2) may
// hold spaces and parentheses, so fields are counted from the last ')'.
func readProcStat(pid int) (state byte, pgid int, start uint64, ok bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, 0, false
	}
	s := string(data)
	end := strings.LastIndexByte(s, ')')
	if end < 0 {
		return 0, 0, 0, false
	}
	f := strings.Fields(s[end+1:])
	// f[0] is field 3: field n is f[n-3].
	if len(f) < 20 || len(f[0]) == 0 {
		return 0, 0, 0, false
	}
	pgid, err1 := strconv.Atoi(f[2])
	start, err2 := strconv.ParseUint(f[19], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, 0, false
	}
	return f[0][0], pgid, start, true
}
