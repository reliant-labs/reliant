// Copyright (c) 2025 Reliant Labs

//go:build unix

package daemonpolicy

import (
	"os"
	"path/filepath"
	"syscall"
)

// trustedGHConfigParent checks the entire path, not just the immediate parent:
// a private directory under a replaceable ancestor is still replaceable.
func trustedGHConfigParent(base string) bool {
	if !filepath.IsAbs(base) {
		return false
	}
	for path := filepath.Clean(base); ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return false // includes symlinks
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (int(st.Uid) != os.Getuid() && st.Uid != 0) {
			return false
		}
		if path == filepath.Clean(base) {
			if !ownedByCurrentUser(info) || info.Mode().Perm()&0o077 != 0 {
				return false
			}
		} else if info.Mode().Perm()&0o022 != 0 {
			// A root-owned sticky directory (e.g. /tmp) protects the
			// current user's entry against rename by other users.
			if st.Uid != 0 || info.Mode()&os.ModeSticky == 0 {
				return false
			}
		}
		if filepath.Dir(path) == path {
			return true
		}
	}
}
