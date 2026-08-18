// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package daemonstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// LockFileName is the basename of the data dir's exclusive claim.
const LockFileName = "daemon.lock"

// ErrLocked reports that another live process already holds the data
// directory. It is the signal that a second daemon must not start.
var ErrLocked = errors.New("daemon data directory is held by a running daemon")

// Lock is a held exclusive claim on a daemon data directory.
//
// The claim is an OS advisory lock on <dataDir>/daemon.lock, NOT the runtime
// record. That distinction is the whole point: the record is a file this
// program writes and deletes, so it can say "no daemon" while a daemon is very
// much alive, and it can say "daemon" long after one died. The kernel drops an
// advisory lock when its holder dies, whatever killed it — so the lock cannot
// go stale, cannot be confused by PID reuse, and cannot be invalidated by
// deleting a file.
//
// It claims a data DIRECTORY, not a daemon identity. Two daemons pointed at
// different data dirs still collide on their gateway daemonID; that collision
// is the gateway's to resolve (it supersedes the incumbent), and this lock does
// not attempt it.
type Lock struct {
	file *os.File
	// path is retained for the error message; the lock lives on the open file
	// description, not on the name.
	path string
}

// Acquire takes the exclusive claim on dataDir, creating the directory if
// needed. It returns ErrLocked when a live process already holds it.
//
// An empty dataDir means the daemon runtime is embedded in another process and
// publishes nothing; there is no directory to claim, so the returned Lock is
// inert and Release is a no-op.
func Acquire(dataDir string) (*Lock, error) {
	if dataDir == "" {
		return &Lock{}, nil
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating daemon data dir: %w", err)
	}
	path := filepath.Join(dataDir, LockFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening daemon lock %s: %w", path, err)
	}
	held, err := tryLockExclusive(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	if !held {
		file.Close()
		return nil, fmt.Errorf("%w (%s)", ErrLocked, path)
	}

	// The PID is written for a human reading the file; nothing reads it back.
	// Truncate first so a shorter PID cannot leave a longer one's tail behind.
	if err := file.Truncate(0); err == nil {
		_, _ = file.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{file: file, path: path}, nil
}

// Release drops the claim. Safe on an inert lock and safe to call twice.
//
// The lock file is deliberately left on disk: removing it would unlink the name
// another process may already have open, and that process would then hold a
// lock on an inode nobody else can reach — two daemons, both convinced they own
// the directory.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	err := unlock(file)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Path reports the lock file's location, or "" for an inert lock.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
