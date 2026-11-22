package daemon

import "golang.org/x/sys/windows"

// renameNoReplace renames oldpath to newpath only while newpath does not
// exist, in one call: MoveFileEx without MOVEFILE_REPLACE_EXISTING refuses an
// existing destination.
func renameNoReplace(oldpath, newpath string) error {
	from, err := windows.UTF16PtrFromString(oldpath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(newpath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, 0)
}
