//go:build linux

package worktreereclaim

import (
	"fmt"
	"os"
	"syscall"
)

// ctime catches edits that restore both bytes and mtime between reconciliations.
// It is never used as an age to backdate inactivity.
func activityFileMetadata(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, stat.Ctim.Sec, stat.Ctim.Nsec)
	}
	return "unknown"
}
