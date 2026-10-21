// Copyright (c) 2025 Reliant Labs
//go:build !windows

package worktreereclaim

import (
	"os"
	"syscall"
)

// ownedByMe reports whether p belongs to this process's user. git refuses a
// repository owned by anyone else (safe.directory), so the file readers in
// gitfiles.go leave such a repository to git, which says so.
func ownedByMe(p string) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
