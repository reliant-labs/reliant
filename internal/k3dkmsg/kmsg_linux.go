//go:build linux

package k3dkmsg

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// statDevice classifies path. Lstat, so a symlink is "other" and gets replaced
// rather than followed.
func statDevice(path string) (State, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return StateMissing, nil
	}
	if err != nil {
		return StateMissing, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if fi.Mode()&fs.ModeCharDevice != 0 && ok && unix.Major(uint64(st.Rdev)) == 1 && unix.Minor(uint64(st.Rdev)) == 11 {
		return StateKmsg, nil
	}
	return StateOther, nil
}
