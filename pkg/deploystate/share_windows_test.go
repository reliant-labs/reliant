//go:build windows

package deploystate

import (
	"io/fs"
	"syscall"
	"testing"
)

// TestIsTransientShareErr pins which failures retryShared waits out: the two
// a replace-by-rename produces while another handle is open, wrapped as os
// wraps them — and nothing else, so a missing file still fails at once.
func TestIsTransientShareErr(t *testing.T) {
	for _, c := range []struct {
		errno syscall.Errno
		want  bool
	}{
		{errorSharingViolation, true},
		{errorAccessDenied, true},
		{syscall.ERROR_FILE_NOT_FOUND, false},
		{syscall.ERROR_PATH_NOT_FOUND, false},
	} {
		err := &fs.PathError{Op: "open", Path: "x.json", Err: c.errno}
		if got := isTransientShareErr(err); got != c.want {
			t.Errorf("isTransientShareErr(%v) = %v, want %v", c.errno, got, c.want)
		}
	}
}
