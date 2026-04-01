// Copyright (c) 2025 Reliant Labs
//go:build !windows

package osutil

import (
	"os/exec"
	"strconv"
	"time"
)

// IsProcessRunning checks if a process with the given PID is still running.
func IsProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Use kill -0 to check if process exists without actually killing it.
	cmd := exec.Command("kill", "-0", strconv.Itoa(pid))
	return cmd.Run() == nil
}

// GenerateProcessSignature creates a signature for a process based on command and start time.
func GenerateProcessSignature(command string, startTime time.Time) string {
	return generateProcessSignature(command, startTime)
}

// ValidateProcessSignature checks if the process at the given PID matches the expected signature.
// This prevents reconnecting to a different process that happens to have the same PID.
func ValidateProcessSignature(pid int, expectedSignature string, command string, startTime time.Time) bool {
	if !IsProcessRunning(pid) {
		return false
	}
	actualSignature := GenerateProcessSignature(command, startTime)
	return actualSignature == expectedSignature
}
