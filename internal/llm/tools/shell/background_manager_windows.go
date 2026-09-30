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

// childPIDSnapshot reads the whole process table once and returns a
// parent PID -> child PIDs map.
//
// One CIM query replaces the recursive per-node wmic descent, which forked a
// process for every node of every tree it walked. wmic is also deprecated on
// current Windows, so Get-CimInstance is the supported way to ask.
func childPIDSnapshot() map[int][]int {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"Get-CimInstance Win32_Process | ForEach-Object { \"$($_.ProcessId) $($_.ParentProcessId)\" }")
	output, err := cmd.Output()
	if err != nil {
		logging.Debug("process-table snapshot failed", "error", err)
		return nil
	}

	children := make(map[int][]int)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	return children
}

// getPortsForPids gets listening ports for many PIDs from ONE netstat run,
// returning them keyed by the PID that owns them.
//
// netstat -ano always dumps the whole connection table, so the previous
// per-PID version paid for that full dump once per process and threw away every
// row but one PID's. Scanning it once and bucketing by PID makes the cost fixed.
func getPortsForPids(pids []int) (map[int][]PortInfo, error) {
	ports := make(map[int][]PortInfo, len(pids))
	if len(pids) == 0 {
		return ports, nil
	}

	wanted := make(map[int]bool, len(pids))
	for _, pid := range pids {
		wanted[pid] = true
	}

	cmd := exec.Command("netstat", "-ano")
	output, err := cmd.Output()
	if err != nil {
		logging.Debug("netstat failed", "error", err)
		return nil, err
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		// netstat -ano output format: Proto  Local Address  Foreign Address  State  PID
		if len(fields) < 5 {
			continue
		}

		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || !wanted[pid] {
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

		ports[pid] = append(ports[pid], PortInfo{
			Port:     port,
			Address:  host,
			Protocol: strings.ToLower(fields[0]),
		})
	}

	return ports, nil
}
