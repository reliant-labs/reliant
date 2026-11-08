// Copyright (c) 2026 Reliant Labs
//go:build linux

package shell

import (
	"os"
	"strconv"
	"strings"
)

// isZombie reports whether pid is dead and awaiting its reaper.
func isZombie(pid int) bool {
	state, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	return err == nil && strings.Contains(string(state), ") Z ")
}
