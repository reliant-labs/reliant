// Copyright (c) 2025 Reliant Labs
//go:build !windows

package pkgmgr

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/osutil"
)

// IsProcessRunning checks if a process with the given PID is still running
func IsProcessRunning(pid int) bool {
	return osutil.IsProcessRunning(pid)
}

// ValidateProcessSignature checks if the process at the given PID matches the expected signature
// This prevents reconnecting to a different process that happens to have the same PID
func ValidateProcessSignature(pid int, expectedSignature string, command string, startTime time.Time) bool {
	return osutil.ValidateProcessSignature(pid, expectedSignature, command, startTime)
}

// GenerateProcessSignature creates a signature for a process based on command and start time
func GenerateProcessSignature(command string, startTime time.Time) string {
	return osutil.GenerateProcessSignature(command, startTime)
}

// GetProcessStartTime attempts to get the start time of a process
// On Unix systems, we use ps to get the process start time
func GetProcessStartTime(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, fmt.Errorf("invalid PID: %d", pid)
	}

	// Use ps to get the process start time
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	output, err := cmd.Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get process start time: %w", err)
	}

	// Parse the lstart format: "Mon Jan  2 15:04:05 2006"
	startTimeStr := strings.TrimSpace(string(output))
	if startTimeStr == "" {
		return time.Time{}, fmt.Errorf("empty start time for PID %d", pid)
	}

	// Try common formats
	formats := []string{
		"Mon Jan  2 15:04:05 2006",
		"Mon Jan 2 15:04:05 2006",
		time.ANSIC,
		time.UnixDate,
	}

	for _, format := range formats {
		if t, err := time.Parse(format, startTimeStr); err == nil {
			return t, nil
		}
	}

	// If we can't parse, just verify the process exists
	return time.Time{}, fmt.Errorf("could not parse start time: %s", startTimeStr)
}

// KillProcess terminates a process by PID
func KillProcess(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid PID: %d", pid)
	}

	cmd := exec.Command("kill", strconv.Itoa(pid))
	return cmd.Run()
}

// KillProcessTree terminates a process and all its children
func KillProcessTree(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid PID: %d", pid)
	}

	// First kill children using pkill
	pkillCmd := exec.Command("pkill", "-P", strconv.Itoa(pid))
	_ = pkillCmd.Run() // Ignore error if no children

	// Then kill the main process
	return KillProcess(pid)
}
