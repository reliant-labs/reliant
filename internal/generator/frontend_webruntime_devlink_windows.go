//go:build windows

package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createDirLink prefers a relative symlink (works under Developer Mode and
// keeps the link free of absolute paths) and falls back to a junction.
func createDirLink(rel, abs, link string) error {
	return symlinkOrJunction(rel, abs, link, os.Symlink, createJunction)
}

// linkMatches also accepts a junction, whose Readlink result is the absolute
// target (os.Readlink strips the \??\ prefix, file_windows.go normaliseLinkPath)
// and whose drive-letter case may differ.
func linkMatches(existing, rel, abs string) bool {
	if filepath.ToSlash(existing) == rel {
		return true
	}
	return filepath.IsAbs(existing) && strings.EqualFold(filepath.Clean(existing), filepath.Clean(abs))
}

// createJunction makes link a directory junction to the absolute directory
// target. No privilege is needed.
func createJunction(target, link string) (err error) {
	target, err = junctionTarget(filepath.Clean(target))
	if err != nil {
		return err
	}
	if err := os.Mkdir(link, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(link)
		}
	}()
	p, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return fmt.Errorf("open %s for junction: %w", link, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	buf := mountPointReparseBuffer(target)
	var returned uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT,
		(*byte)(unsafe.Pointer(&buf[0])), uint32(len(buf)), nil, 0, &returned, nil); err != nil {
		return fmt.Errorf("create junction %s -> %s: %w", link, target, err)
	}
	return nil
}
