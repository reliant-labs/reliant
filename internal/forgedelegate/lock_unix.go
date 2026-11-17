//go:build unix && !aix

package forgedelegate

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// entryLocks says lockEntry really excludes other processes, so the holder
// may treat what it finds in the entry as abandoned.
const entryLocks = true

// lockEntryPoll is how often a waiter retries the entry's lock.
var lockEntryPoll = 250 * time.Millisecond

// lockEntryContended, when set, is called each time lockEntry finds the lock
// held by someone else. Tests use it to know a caller is waiting on the lock.
var lockEntryContended func(path string)

// lockEntry takes an exclusive flock(2) on path, waiting until it is free or
// ctx ends. The kernel drops the lock when the holder exits, however it
// exits, so a killed builder never wedges the entry. A ctx that has ended
// never gets the lock, even a free one.
func lockEntry(ctx context.Context, path string) (unlock func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			unlock := func() {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				_ = f.Close()
			}
			if err := ctx.Err(); err != nil {
				unlock()
				return nil, err
			}
			return unlock, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			_ = f.Close()
			return nil, err
		}
		if errors.Is(err, unix.EWOULDBLOCK) && lockEntryContended != nil {
			lockEntryContended(path)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(lockEntryPoll):
		}
	}
}
