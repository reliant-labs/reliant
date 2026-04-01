// Copyright (c) 2025 Reliant Labs
// Package pidlock provides cross-platform PID file locking to ensure only one
// instance of a process runs per data directory. This prevents zombie processes
// from holding database locks.
package pidlock

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// DefaultLockFileName is the default name for the lock file
	DefaultLockFileName = ".reliant-backend.lock"

	// MaxRetryAttempts is the number of times to retry acquiring the lock
	MaxRetryAttempts = 3

	// RetryDelay is the time to wait between retry attempts
	RetryDelay = 2 * time.Second

	// KillGracePeriod is the time to wait for a process to exit after SIGTERM
	KillGracePeriod = 5 * time.Second
)

// Lock represents a PID file lock
type Lock struct {
	path string
	file *os.File
}

// New creates a new Lock for the given data directory
func New(dataDir string) *Lock {
	return &Lock{
		path: filepath.Join(dataDir, DefaultLockFileName),
	}
}

// NewWithPath creates a new Lock with a specific file path
func NewWithPath(lockPath string) *Lock {
	return &Lock{
		path: lockPath,
	}
}

// Path returns the lock file path
func (l *Lock) Path() string {
	return l.path
}

// AcquireWithRetry attempts to acquire the lock, killing any stale process if needed.
// It will retry up to MaxRetryAttempts times.
func (l *Lock) AcquireWithRetry() error {
	var lastErr error

	for attempt := 1; attempt <= MaxRetryAttempts; attempt++ {
		err := l.tryAcquire()
		if err == nil {
			if attempt > 1 {
				logging.Info("PID lock acquired after retry", "attempt", attempt, "path", l.path)
			} else {
				logging.Info("PID lock acquired", "path", l.path)
			}
			return nil
		}

		lastErr = err
		logging.Warn("Failed to acquire PID lock",
			"attempt", attempt,
			"maxAttempts", MaxRetryAttempts,
			"path", l.path,
			"error", err)

		// If this isn't our last attempt, try to kill the stale process
		if attempt < MaxRetryAttempts {
			if killErr := l.killStaleProcess(); killErr != nil {
				logging.Warn("Failed to kill stale process", "error", killErr)
			}
			time.Sleep(RetryDelay)
		}
	}

	return fmt.Errorf("failed to acquire PID lock after %d attempts: %w", MaxRetryAttempts, lastErr)
}

// tryAcquire attempts to acquire the lock once
func (l *Lock) tryAcquire() error {
	// Ensure directory exists
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create lock directory: %w", err)
	}

	// Open or create the lock file
	file, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("failed to open lock file: %w", err)
	}

	// Try to acquire exclusive lock (non-blocking)
	if err := l.tryLock(file); err != nil {
		file.Close()
		return err
	}

	// Lock acquired - write our PID
	if err := file.Truncate(0); err != nil {
		file.Close()
		return fmt.Errorf("failed to truncate lock file: %w", err)
	}

	pid := os.Getpid()
	if _, err := fmt.Fprintf(file, "%d\n", pid); err != nil {
		file.Close()
		return fmt.Errorf("failed to write PID to lock file: %w", err)
	}

	// Sync to ensure PID is written
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("failed to sync lock file: %w", err)
	}

	l.file = file
	return nil
}

// Release releases the lock and removes the lock file
func (l *Lock) Release() error {
	if l.file == nil {
		return nil
	}

	// Unlock the file
	if err := l.unlock(l.file); err != nil {
		logging.Warn("Failed to unlock file", "error", err)
	}

	// Close the file
	if err := l.file.Close(); err != nil {
		logging.Warn("Failed to close lock file", "error", err)
	}

	// Remove the lock file
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		logging.Warn("Failed to remove lock file", "path", l.path, "error", err)
	}

	l.file = nil
	logging.Info("PID lock released", "path", l.path)
	return nil
}

// GetLockedPID reads the PID from the lock file without acquiring the lock.
// Returns 0 if the file doesn't exist or can't be read.
func (l *Lock) GetLockedPID() int {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return 0
	}

	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0
	}

	return pid
}

// IsLocked checks if the lock file is currently held by another process.
// This is a non-blocking check.
func (l *Lock) IsLocked() bool {
	file, err := os.OpenFile(l.path, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer file.Close()

	err = l.tryLock(file)
	if err != nil {
		return true // Lock is held
	}

	// We got the lock - release it immediately
	if err := l.unlock(file); err != nil {
		logging.Warn("Failed to unlock test lock file", "error", err)
	}
	return false
}
