package daemon

import "golang.org/x/sys/unix"

// renameNoReplace renames oldpath to newpath only while newpath does not
// exist, in one call: renameat2(2) with RENAME_NOREPLACE. A filesystem that
// does not support the flag fails it, and nothing is renamed.
func renameNoReplace(oldpath, newpath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
}
