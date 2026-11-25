// Copyright (c) 2026 Reliant Labs
//go:build linux

package osutil

import (
	"os"
	"strconv"
	"strings"
)

// IdentifyProcess reads pid's parent, process group, start time and whether
// it is a zombie from /proc/<pid>/stat. ok is false when there is no such
// process (or it was reaped).
func IdentifyProcess(pid int) (ProcessIdentity, bool) {
	if pid <= 0 {
		return ProcessIdentity{}, false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ProcessIdentity{}, false
	}
	// The command name (field 2) may hold spaces and parentheses, so fields
	// are counted from the last ')': f[0] is field 3, field n is f[n-3].
	s := string(data)
	end := strings.LastIndexByte(s, ')')
	if end < 0 {
		return ProcessIdentity{}, false
	}
	f := strings.Fields(s[end+1:])
	if len(f) < 20 || len(f[0]) == 0 {
		return ProcessIdentity{}, false
	}
	ppid, err1 := strconv.Atoi(f[1])
	pgid, err2 := strconv.Atoi(f[2])
	start, err3 := strconv.ParseUint(f[19], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return ProcessIdentity{}, false
	}
	state := f[0][0]
	if state == 'X' {
		return ProcessIdentity{}, false
	}
	return ProcessIdentity{PPID: ppid, PGID: pgid, Start: start, Exited: state == 'Z'}, true
}
