//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"syscall"
	"time"
)

const (
	errorAccessDenied     syscall.Errno = 5  // ERROR_ACCESS_DENIED
	errorSharingViolation syscall.Errno = 32 // ERROR_SHARING_VIOLATION
)

const replaceRetryBudget = 2 * time.Second

// replaceFile is os.Rename (MoveFileEx with MOVEFILE_REPLACE_EXISTING), retried
// while another handle — an editor, an indexer, a virus scanner — has the
// target open. Windows refuses the replace for that instant only; the old file
// stays in place until the replace succeeds, so a persistent refusal leaves the
// old content intact rather than a partial new one.
func replaceFile(oldPath, newPath string) error {
	deadline := time.Now().Add(replaceRetryBudget)
	delay := time.Millisecond
	for {
		err := os.Rename(oldPath, newPath)
		if err == nil || !transient(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
}

func transient(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == errorAccessDenied || errno == errorSharingViolation)
}
