//go:build !windows

package ledgerfile

import (
	"os"
	"syscall"
)

// lockExclusive takes a blocking exclusive advisory lock on f.
//
// flock(2) is tied to the OPEN FILE DESCRIPTION rather than the process, so
// the kernel drops the lock when the descriptor closes — including on
// abnormal termination. That is the property this store depends on: a forge
// that is killed mid-promote never wedges the ledger for every later
// command. The same reasoning, and the same call, as pkg/pgtest's pool lock.
//
// It BLOCKS (no LOCK_NB) deliberately. A promote that has to wait a few
// milliseconds for a concurrent one is correct; a promote that fails because
// another was in flight would make honest concurrency look like an error.
func lockExclusive(f *os.File) (unlock func(), err error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}
