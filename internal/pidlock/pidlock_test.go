// Copyright (c) 2025 Reliant Labs
package pidlock

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLockAcquireRelease(t *testing.T) {
	// Create a temp directory for the test
	tmpDir, err := os.MkdirTemp("", "pidlock-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	lock := New(tmpDir)

	// Acquire the lock
	if err := lock.AcquireWithRetry(); err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Verify lock file exists and contains our PID
	lockPath := filepath.Join(tmpDir, DefaultLockFileName)
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Fatal("Lock file should exist after acquiring lock")
	}

	pid := lock.GetLockedPID()
	if pid != os.Getpid() {
		t.Errorf("Lock file PID = %d, want %d", pid, os.Getpid())
	}

	// Release the lock
	if err := lock.Release(); err != nil {
		t.Errorf("Failed to release lock: %v", err)
	}

	// Verify lock file is removed
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("Lock file should be removed after releasing lock")
	}
}

func TestLockConflict(t *testing.T) {
	// Create a temp directory for the test
	tmpDir, err := os.MkdirTemp("", "pidlock-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Acquire the first lock
	lock1 := New(tmpDir)
	if err := lock1.AcquireWithRetry(); err != nil {
		t.Fatalf("Failed to acquire first lock: %v", err)
	}
	defer lock1.Release()

	// Try to acquire a second lock - this should fail immediately
	// (we use tryAcquire directly to avoid the kill-and-retry logic)
	lock2 := New(tmpDir)
	err = lock2.tryAcquire()
	if err == nil {
		lock2.Release()
		t.Fatal("Second lock acquisition should have failed")
	}

	// Verify IsLocked returns true
	lock3 := New(tmpDir)
	if !lock3.IsLocked() {
		t.Error("IsLocked should return true when lock is held")
	}
}

func TestLockFileContents(t *testing.T) {
	// Create a temp directory for the test
	tmpDir, err := os.MkdirTemp("", "pidlock-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	lock := New(tmpDir)
	if err := lock.AcquireWithRetry(); err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}
	defer lock.Release()

	// Read the lock file directly
	lockPath := filepath.Join(tmpDir, DefaultLockFileName)
	content, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("Failed to read lock file: %v", err)
	}

	// Should contain PID followed by newline
	expectedPID := os.Getpid()
	var actualPID int
	if _, err := fmt.Sscanf(string(content), "%d\n", &actualPID); err != nil {
		t.Errorf("Lock file content is not a valid PID: %q", string(content))
	}
	if actualPID != expectedPID {
		t.Errorf("Lock file PID = %d, want %d", actualPID, expectedPID)
	}
}

func TestGetLockedPIDNonExistent(t *testing.T) {
	// Create a temp directory for the test
	tmpDir, err := os.MkdirTemp("", "pidlock-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	lock := New(tmpDir)

	// Should return 0 for non-existent lock file
	pid := lock.GetLockedPID()
	if pid != 0 {
		t.Errorf("GetLockedPID() = %d, want 0 for non-existent lock file", pid)
	}
}
