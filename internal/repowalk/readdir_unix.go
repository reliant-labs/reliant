//go:build unix

package repowalk

import (
	"io/fs"
	"os"
	"syscall"
)

// readDirNoLog lists dir through a descriptor os.Open never handed out, so
// the read is not recorded in the test log (os.Open and os.Stat record; a
// File's ReadDir does not).
func readDirNoLog(dir string) ([]fs.DirEntry, error) {
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: dir, Err: err}
	}
	f := os.NewFile(uintptr(fd), dir)
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
}
