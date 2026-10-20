// Copyright (c) 2025 Reliant Labs
//go:build linux

package osutil

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ParentPID returns pid's parent process id, read from /proc/<pid>/stat.
func ParentPID(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// The second field is the command name in parentheses, and it may itself
	// contain spaces and parentheses; everything after the LAST ')' is
	// space-separated: state, then ppid.
	stat := string(data)
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	return strconv.Atoi(fields[1])
}
