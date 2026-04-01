// Copyright (c) 2025 Reliant Labs
//go:build !windows

package shell

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/reliant-labs/reliant/internal/logging"
)

// setProcessGroup sets up the command to run in its own process group.
// This allows us to kill the entire process tree when terminating.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends a signal to the entire process group.
// On Unix, we use negative PID to signal the process group.
func killProcessGroup(pid int, signal syscall.Signal) error {
	// Negative PID signals the entire process group
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		// If we can't get the pgid, try killing just the process
		logging.Warn("Failed to get process group ID, killing single process",
			"pid", pid,
			"error", err)
		return syscall.Kill(pid, signal)
	}

	logging.Debug("Killing process group",
		"pid", pid,
		"pgid", pgid,
		"signal", signal)

	// Kill the entire process group using negative pgid
	return syscall.Kill(-pgid, signal)
}

// terminateProcessGroup gracefully terminates a process group.
// First sends SIGTERM, then SIGKILL if needed.
func terminateProcessGroup(pid int) error {
	// First try SIGTERM for graceful shutdown
	err := killProcessGroup(pid, syscall.SIGTERM)
	if err != nil {
		logging.Warn("Failed to send SIGTERM to process group",
			"pid", pid,
			"error", err)
		// Try SIGKILL as fallback
		return killProcessGroup(pid, syscall.SIGKILL)
	}
	return nil
}

// forceKillProcessGroup forcefully kills a process group with SIGKILL.
func forceKillProcessGroup(pid int) error {
	return killProcessGroup(pid, syscall.SIGKILL)
}

// createShellCommand creates an exec.Cmd for running a shell command.
// On Unix, this uses bash -c.
func createShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "bash", "-c", command)
}

// getProcessTree returns all PIDs in the process tree (parent + all descendants).
// On Unix, uses pgrep to find child processes recursively.
func getProcessTree(pid int) []int {
	pids := []int{pid}

	// Use pgrep to find child processes
	// -P: match parent PID
	cmd := exec.Command("pgrep", "-P", strconv.Itoa(pid))
	output, err := cmd.Output()
	if err != nil {
		return pids
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		childPid, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err == nil {
			// Recursively get children of this child
			pids = append(pids, getProcessTree(childPid)...)
		}
	}

	return pids
}

// getPortsForPid gets ports for a single PID using lsof.
// On Unix, uses lsof to get open network connections.
func getPortsForPid(pid int) ([]PortInfo, error) {
	var ports []PortInfo

	// Use lsof to get open network connections for the process
	// -P: don't convert port numbers to names
	// -n: don't convert IP addresses to names
	// -i: show network connections
	// -a: AND the conditions
	cmd := exec.Command("lsof", "-P", "-n", "-i", "-a", "-p", strconv.Itoa(pid))
	output, err := cmd.Output()
	if err != nil {
		// lsof returns error if no files found, which is ok
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return ports, nil
		}
		return nil, err
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	// Skip the header line
	if scanner.Scan() {
		// Process each line
		for scanner.Scan() {
			line := scanner.Text()
			portInfo := parsePortInfo(line)
			if portInfo != nil {
				ports = append(ports, *portInfo)
			}
		}
	}

	return ports, nil
}

// parsePortInfo parses a line from lsof output.
func parsePortInfo(line string) *PortInfo {
	// lsof output format:
	// COMMAND   PID USER   FD   TYPE    DEVICE SIZE/OFF NODE NAME
	// node    12345 user   23u  IPv4 0x1234567      0t0  TCP *:3000 (LISTEN)

	fields := strings.Fields(line)
	if len(fields) < 9 {
		return nil
	}

	// Get the NAME field (last field)
	name := fields[len(fields)-1]
	state := ""

	// Check if there's a state in parentheses
	if len(fields) >= 10 && strings.HasPrefix(fields[len(fields)-1], "(") {
		state = strings.Trim(fields[len(fields)-1], "()")
		name = fields[len(fields)-2]
	}

	// Parse protocol and address:port
	protocol := strings.ToLower(fields[7])
	if protocol != "tcp" && protocol != "udp" {
		// Try to extract from TYPE field
		typeField := fields[4]
		if strings.Contains(strings.ToLower(typeField), "tcp") {
			protocol = "tcp"
		} else if strings.Contains(strings.ToLower(typeField), "udp") {
			protocol = "udp"
		}
	}

	// Parse address and port from NAME field
	// Format can be: *:3000, 127.0.0.1:3000, [::]:3000, etc.
	parts := strings.Split(name, ":")
	if len(parts) < 2 {
		return nil
	}

	portStr := parts[len(parts)-1]

	// Handle case where port might be followed by ->remote:port
	if strings.Contains(portStr, "->") {
		portStr = strings.Split(portStr, "->")[0]
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil
	}

	// Only return LISTEN ports (not outbound connections)
	if state != "LISTEN" {
		return nil
	}

	address := strings.Join(parts[:len(parts)-1], ":")
	if address == "*" {
		address = "0.0.0.0"
	}

	return &PortInfo{
		Port:     port,
		Protocol: protocol,
		State:    state,
		Address:  address,
	}
}
