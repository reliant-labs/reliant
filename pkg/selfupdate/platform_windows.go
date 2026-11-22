//go:build windows

package selfupdate

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// swapSupported: a running .exe cannot be replaced, but it can be renamed,
// so the swap moves it aside first (runningImageLocked), and the new binary
// starts as a new process (execBinary).
func swapSupported() bool { return true }

// runningImageLocked: Windows maps a running image without FILE_SHARE_WRITE,
// so neither a rename over it nor a remove of it succeeds while it runs. It
// may still be renamed, which is what the swap does.
const runningImageLocked = true

// hostRename is os.Rename (MoveFileEx, replacing an existing target) with
// MOVEFILE_WRITE_THROUGH: it returns once the rename is on disk, so the move
// journal written before it, and each rename of a swap, reach the disk in
// order.
func hostRename(oldpath, newpath string) error {
	from, err := windows.UTF16PtrFromString(oldpath)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	to, err := windows.UTF16PtrFromString(newpath)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	return nil
}

// execBinary starts the binary at path as a new process with the same argv,
// environment, working directory and stdio, then exits this one: Windows has
// no exec(2). The pid changes, and this process's children are orphaned. It
// returns only if the new process could not be started.
func execBinary(path string, argv, env []string) error {
	return respawn(path, argv, env, (*exec.Cmd).Start, os.Exit)
}

// isTextBusy: Windows has no ETXTBSY.
func isTextBusy(error) bool { return false }

const (
	processQueryLimitedInformation = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION
	stillActive                    = 259    // STILL_ACTIVE
)

// processAlive reports whether pid names a running process. Access denied
// means alive, but another user's; any other failure to open it means gone.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true // cannot tell: never count a boot it cannot see die
	}
	return code == stillActive
}

// syncDir is a no-op: Windows cannot open a directory for FlushFileBuffers
// without backup semantics, and NTFS journals the rename itself.
func syncDir(string) error { return nil }
