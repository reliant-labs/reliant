// Copyright (c) 2025 Reliant Labs
//go:build windows

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
// On Windows, we use CREATE_NEW_PROCESS_GROUP flag.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// killProcessGroup sends a termination signal to the process tree.
// On Windows, we use taskkill with /T to kill the process tree.
func killProcessGroup(pid int, signal syscall.Signal) error {
	// Use taskkill to kill the process tree
	// /T kills the process and all child processes
	// /F forces termination
	cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	err := cmd.Run()
	if err != nil {
		logging.Warn("taskkill failed",
			"pid", pid,
			"error", err)
		return err
	}
	return nil
}

// terminateProcessGroup gracefully terminates a process group.
// On Windows, we just use taskkill which forcefully terminates.
func terminateProcessGroup(pid int) error {
	return killProcessGroup(pid, 0) // Signal is ignored on Windows
}

// forceKillProcessGroup forcefully kills a process group.
// On Windows, this is the same as terminateProcessGroup.
func forceKillProcessGroup(pid int) error {
	return killProcessGroup(pid, 0) // Signal is ignored on Windows
}

// createShellCommand creates an exec.Cmd for running a shell command.
// On Windows, this uses PowerShell with -NoProfile -Command.
func createShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", command)
}

// getProcessTree returns all PIDs in the process tree (parent + all descendants).
// On Windows, uses wmic to enumerate child processes recursively.
func getProcessTree(pid int) []int {
	pids := []int{pid}
	children := getChildPids(pid)
	for _, child := range children {
		pids = append(pids, getProcessTree(child)...)
	}
	return pids
}

// getChildPids returns immediate child PIDs for a given parent PID.
func getChildPids(parentPid int) []int {
	// Use wmic to query child processes
	cmd := exec.Command("wmic", "process", "where", "ParentProcessId="+strconv.Itoa(parentPid), "get", "ProcessId")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var children []int
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "ProcessId" {
			continue
		}
		if childPid, err := strconv.Atoi(line); err == nil {
			children = append(children, childPid)
		}
	}
	return children
}

// getPortsForPid gets ports for a single PID using netstat.
// Parses output of "netstat -ano" to find listening ports for the given PID.
func getPortsForPid(pid int) ([]PortInfo, error) {
	cmd := exec.Command("netstat", "-ano")
	output, err := cmd.Output()
	if err != nil {
		logging.Debug("netstat failed", "pid", pid, "error", err)
		return nil, err
	}

	pidStr := strconv.Itoa(pid)
	var ports []PortInfo

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		// netstat -ano output format: Proto  Local Address  Foreign Address  State  PID
		if len(fields) < 5 {
			continue
		}

		// Check if this line is for our PID and is LISTENING
		if fields[len(fields)-1] != pidStr {
			continue
		}
		if len(fields) >= 4 && fields[3] != "LISTENING" {
			continue
		}

		// Parse local address (e.g., "0.0.0.0:8080" or "[::]:8080")
		localAddr := fields[1]
		lastColon := strings.LastIndex(localAddr, ":")
		if lastColon == -1 {
			continue
		}

		portStr := localAddr[lastColon+1:]
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}

		host := localAddr[:lastColon]
		// Clean up IPv6 brackets
		host = strings.Trim(host, "[]")
		if host == "0.0.0.0" || host == "::" || host == "*" {
			host = "localhost"
		}

		ports = append(ports, PortInfo{
			Port:     port,
			Address:  host,
			Protocol: strings.ToLower(fields[0]),
		})
	}

	return ports, nil
}
