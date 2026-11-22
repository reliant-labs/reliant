package daemon

import "golang.org/x/sys/unix"

// renameNoReplace renames oldpath to newpath only while newpath does not
// exist, in one call: renamex_np(2) with RENAME_EXCL.
func renameNoReplace(oldpath, newpath string) error {
	return unix.RenamexNp(oldpath, newpath, unix.RENAME_EXCL)
}
