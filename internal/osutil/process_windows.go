// Copyright (c) 2025 Reliant Labs
//go:build windows

package osutil

import (
	"time"

	"golang.org/x/sys/windows"
)

// IsProcessRunning checks if a process with the given PID is still running.
func IsProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}

	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}

	// 259 is STILL_ACTIVE (PROCESS_STILL_ACTIVE)
	return exitCode == 259
}

// GenerateProcessSignature creates a signature for a process based on command and start time.
func GenerateProcessSignature(command string, startTime time.Time) string {
	return generateProcessSignature(command, startTime)
}

// ValidateProcessSignature checks if the process at the given PID matches the expected signature.
func ValidateProcessSignature(pid int, expectedSignature string, command string, startTime time.Time) bool {
	if !IsProcessRunning(pid) {
		return false
	}
	actualSignature := GenerateProcessSignature(command, startTime)
	return actualSignature == expectedSignature
}
