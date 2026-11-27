//go:build !windows

package gocacheprog

import (
	"fmt"
	"os"
	"syscall"
)

func checkOwner(dir string, info os.FileInfo) error {
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("trusted cache %s is group- or world-writable (%#o): another user could have planted entries; chmod go-w it, or use a fresh directory", dir, perm)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := os.Getuid(); int(st.Uid) != uid {
		return fmt.Errorf("trusted cache %s is owned by uid %d, not this user (uid %d): only its owner may seed it", dir, st.Uid, uid)
	}
	return nil
}
