// Copyright (c) 2025 Reliant Labs
//go:build windows

package pidlock

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/reliant-labs/reliant/internal/logging"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
	procOpenProcess  = modkernel32.NewProc("OpenProcess")
	procCloseHandle  = modkernel32.NewProc("CloseHandle")
)

const (
	// LOCKFILE_EXCLUSIVE_LOCK - request an exclusive lock
	LOCKFILE_EXCLUSIVE_LOCK = 0x00000002
	// LOCKFILE_FAIL_IMMEDIATELY - return immediately if lock cannot be acquired
	LOCKFILE_FAIL_IMMEDIATELY = 0x00000001
	// ERROR_LOCK_VIOLATION - Windows error code when lock cannot be acquired
	ERROR_LOCK_VIOLATION = syscall.Errno(33)
)

// tryLock attempts to acquire an exclusive lock on the file (Windows implementation)
func (l *Lock) tryLock(file *os.File) error {
	handle := syscall.Handle(file.Fd())

	// LockFileEx parameters:
	// - hFile: handle to file
	// - dwFlags: lock flags
	// - dwReserved: reserved, must be zero
	// - nNumberOfBytesToLockLow: low-order 32 bits of length
	// - nNumberOfBytesToLockHigh: high-order 32 bits of length
	// - lpOverlapped: pointer to OVERLAPPED structure

	// We need an OVERLAPPED structure for LockFileEx
	var overlapped syscall.Overlapped

	// Lock the entire file (1 byte is enough for a lock file)
	flags := uint32(LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY)

	ret, _, err := procLockFileEx.Call(
		uintptr(handle),
		uintptr(flags),
		0, // reserved
		1, // nNumberOfBytesToLockLow (1 byte)
		0, // nNumberOfBytesToLockHigh
		uintptr(unsafe.Pointer(&overlapped)),
	)

	if ret == 0 {
		// LockFileEx returns 0 on failure
		if err == ERROR_LOCK_VIOLATION {
			return fmt.Errorf("lock is held by another process")
		}
		return fmt.Errorf("LockFileEx failed: %w", err)
	}

	return nil
}

// unlock releases the lock on the file (Windows implementation)
func (l *Lock) unlock(file *os.File) error {
	handle := syscall.Handle(file.Fd())

	var overlapped syscall.Overlapped

	ret, _, err := procUnlockFileEx.Call(
		uintptr(handle),
		0, // reserved
		1, // nNumberOfBytesToUnlockLow
		0, // nNumberOfBytesToUnlockHigh
		uintptr(unsafe.Pointer(&overlapped)),
	)

	if ret == 0 {
		return fmt.Errorf("UnlockFileEx failed: %w", err)
	}

	return nil
}

const (
	// PROCESS_TERMINATE - required to terminate a process
	PROCESS_TERMINATE = 0x0001
	// PROCESS_QUERY_LIMITED_INFORMATION - required to query process info
	PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
)

// processExists checks if a process with the given PID exists on Windows
func processExists(pid int) bool {
	// Try to open the process with minimal permissions
	handle, _, _ := procOpenProcess.Call(
		uintptr(PROCESS_QUERY_LIMITED_INFORMATION),
		0, // bInheritHandle = FALSE
		uintptr(pid),
	)

	if handle == 0 {
		return false
	}

	// Close the handle
	procCloseHandle.Call(handle)
	return true
}

// killStaleProcess attempts to kill the process that holds the lock (Windows implementation)
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
	if !processExists(pid) {
		logging.Debug("Process no longer exists", "pid", pid)
		return nil
	}

	logging.Warn("Killing stale backend process", "pid", pid)

	// On Windows, we can only do a hard kill - there's no graceful SIGTERM equivalent
	process, err := os.FindProcess(pid)
	if err != nil {
		logging.Debug("Process not found", "pid", pid)
		return nil
	}

	// Kill the process
	if err := process.Kill(); err != nil {
		// Check if process already exited
		if !processExists(pid) {
			logging.Info("Stale process already exited", "pid", pid)
			return nil
		}
		return fmt.Errorf("failed to kill process %d: %w", pid, err)
	}

	// Wait a moment for the process to fully terminate
	for i := 0; i < 20; i++ {
		if !processExists(pid) {
			logging.Info("Stale process killed", "pid", pid)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("process %d did not exit after kill", pid)
}
