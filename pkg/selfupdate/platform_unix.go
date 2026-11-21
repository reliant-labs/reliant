//go:build unix

package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// swapSupported: a running binary's path can be renamed over and the new
// one exec'd in place.
func swapSupported() bool { return true }

// execBinary replaces this process with the binary at path, as
// forgedelegate.Exec does: same pid, stdio and signal disposition. It returns
// only on failure.
func execBinary(path string, argv, env []string) error {
	for attempt := 0; ; attempt++ {
		err := syscall.Exec(path, argv, env)
		if !isTextBusy(err) || attempt == 20 {
			return fmt.Errorf("exec %s: %w", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// isTextBusy is ETXTBSY: a child forked elsewhere in this process still
// holds a just-written binary open for writing (golang/go#22315); it closes
// within milliseconds.
func isTextBusy(err error) bool { return errors.Is(err, syscall.ETXTBSY) }

// processAlive reports whether pid names a live process (EPERM: alive, but
// another user's).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// syncDir fsyncs a directory, making a rename in it durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
