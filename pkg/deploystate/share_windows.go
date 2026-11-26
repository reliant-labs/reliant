//go:build windows

package deploystate

import (
	"errors"
	"syscall"
)

// Win32 error codes a replace-by-rename can surface while another handle on
// the target is open. Spelled out because syscall names only the first.
const (
	errorAccessDenied     syscall.Errno = 5  // ERROR_ACCESS_DENIED
	errorSharingViolation syscall.Errno = 32 // ERROR_SHARING_VIOLATION
)

// isTransientShareErr reports whether err is Windows refusing a file
// operation only because another handle has the file open at this instant.
//
// POSIX rename(2) replaces a file atomically however many readers have it
// open. Windows does not: while MoveFileEx is replacing the target, a
// concurrent open fails with ERROR_SHARING_VIOLATION, and the rename itself
// fails with ERROR_ACCESS_DENIED if a reader's handle (Go opens without
// FILE_SHARE_DELETE) is open across it. Both clear as soon as the other side
// closes, so they are retried rather than reported. This is the same set Go's
// own toolchain retries for the same reason (cmd/go/internal/robustio).
func isTransientShareErr(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == errorAccessDenied || errno == errorSharingViolation
}
