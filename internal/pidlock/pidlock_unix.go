// Copyright (c) 2025 Reliant Labs
//go:build !windows

package pidlock

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// tryLock attempts to acquire an exclusive lock on the file (Unix implementation using flock)
func (l *Lock) tryLock(file *os.File) error {
	// LOCK_EX: exclusive lock
	// LOCK_NB: non-blocking (return immediately if lock can't be acquired)
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		if err == syscall.EWOULDBLOCK {
			return fmt.Errorf("lock is held by another process")
		}
		return fmt.Errorf("flock failed: %w", err)
	}
	return nil
}

// unlock releases the lock on the file (Unix implementation)
func (l *Lock) unlock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

// killStaleProcess attempts to kill the process that holds the lock (Unix implementation)
func (l *Lock) killStaleProcess() error {
	pid := l.GetLockedPID()
	if pid == 0 {
		logging.Debug("No PID found in lock file, cannot kill stale process")
		return nil
	}

	// Check if it's our own PID (shouldn't happen, but safety check)
	if pid == os.Getpid() {
		return fmt.Errorf("refusing to kill own process")
	}

	// Check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		logging.Debug("Process not found", "pid", pid)
		return nil
	}

	// On Unix, FindProcess always succeeds - we need to check if process exists
	// by sending signal 0
	if err := process.Signal(syscall.Signal(0)); err != nil {
		logging.Debug("Process no longer exists", "pid", pid, "error", err)
		return nil
	}

	logging.Warn("Killing stale backend process", "pid", pid)

	// Try graceful shutdown first with SIGTERM
	if err := process.Signal(syscall.SIGTERM); err != nil {
		logging.Warn("Failed to send SIGTERM", "pid", pid, "error", err)
	}

	// Wait for process to exit
	exitCh := make(chan struct{})
	go func() {
		for {
			if err := process.Signal(syscall.Signal(0)); err != nil {
				close(exitCh)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	select {
	case <-exitCh:
		logging.Info("Stale process exited gracefully", "pid", pid)
		return nil
	case <-time.After(KillGracePeriod):
		// Process didn't exit, force kill
		logging.Warn("Stale process did not exit gracefully, sending SIGKILL", "pid", pid)
		if err := process.Signal(syscall.SIGKILL); err != nil {
			return fmt.Errorf("failed to send SIGKILL to process %d: %w", pid, err)
		}

		// Wait a bit more for SIGKILL to take effect
		select {
		case <-exitCh:
			logging.Info("Stale process killed", "pid", pid)
			return nil
		case <-time.After(2 * time.Second):
			return fmt.Errorf("process %d did not exit after SIGKILL", pid)
		}
	}
}
