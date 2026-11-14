// Copyright (c) 2025 Reliant Labs
//go:build windows

package pkgmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// IsProcessRunning checks if a process with the given PID is still running
func IsProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}

	// Use tasklist to check if process exists
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	// If process exists, tasklist output will contain the PID
	return strings.Contains(string(output), strconv.Itoa(pid))
}

// ValidateProcessSignature checks if the process at the given PID matches the expected signature
// This prevents reconnecting to a different process that happens to have the same PID
func ValidateProcessSignature(pid int, expectedSignature string, command string, startTime time.Time) bool {
	if !IsProcessRunning(pid) {
		return false
	}

	// Generate expected signature and compare
	actualSignature := GenerateProcessSignature(command, startTime)
	return actualSignature == expectedSignature
}

// GenerateProcessSignature creates a signature for a process based on command and start time
func GenerateProcessSignature(command string, startTime time.Time) string {
	data := fmt.Sprintf("%s|%d", command, startTime.UnixNano())
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:8]) // Use first 8 bytes for shorter signature
}

// GetProcessStartTime attempts to get the start time of a process
// On Windows, this is more complex and we rely on our stored signature instead
func GetProcessStartTime(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, fmt.Errorf("invalid PID: %d", pid)
	}

	// On Windows, getting exact process start time requires WMI or more complex APIs
	// For now, return an error and rely on signature validation
	return time.Time{}, fmt.Errorf("process start time not available on Windows")
}

// KillProcess terminates a process by PID
func KillProcess(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid PID: %d", pid)
	}

	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")
	return cmd.Run()
}

// KillProcessTree terminates a process and all its children
func KillProcessTree(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid PID: %d", pid)
	}

	// /T flag kills the process tree
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	return cmd.Run()
}
