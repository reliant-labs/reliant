//go:build linux

package daemonruntime

import "golang.org/x/sys/unix"

func publishRecoveryDirectory(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}
