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

// childPIDSnapshot reads the whole process table once and returns a
// parent PID -> child PIDs map.
//
// One `ps` replaces the recursive `pgrep -P` descent, which forked once per node
// of every tree it walked. A single snapshot also gives a consistent view: the
// recursive version could see a child that had already exited by the time it
// asked about that child's own children.
func childPIDSnapshot() map[int][]int {
	cmd := exec.Command("ps", "-Ao", "pid=,ppid=")
	output, err := cmd.Output()
	if err != nil {
		logging.Debug("ps process-table snapshot failed", "error", err)
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

// getPortsForPids gets listening ports for many PIDs in ONE lsof invocation,
// returning them keyed by the PID that owns them.
//
// lsof accepts a comma-separated PID list for -p, so the number of subprocesses
// is independent of how many processes are being listed. Measured on macOS, one
// lsof costs ~65ms whether it is asked about one PID or several, so this turns a
// per-PID cost into a fixed one.
func getPortsForPids(pids []int) (map[int][]PortInfo, error) {
	ports := make(map[int][]PortInfo, len(pids))
	if len(pids) == 0 {
		return ports, nil
	}

	pidArgs := make([]string, len(pids))
	for i, pid := range pids {
		pidArgs[i] = strconv.Itoa(pid)
	}

	// -P: don't convert port numbers to names
	// -n: don't convert IP addresses to names
	// -i: show network connections
	// -a: AND the conditions
	cmd := exec.Command("lsof", "-P", "-n", "-i", "-a", "-p", strings.Join(pidArgs, ","))
	output, err := cmd.Output()
	if err != nil {
		// lsof exits 1 when it found nothing matching, which is not an error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return ports, nil
		}
		return nil, err
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	// Skip the header line
	if scanner.Scan() {
		for scanner.Scan() {
			line := scanner.Text()
			// The PID must come from the line itself now that one invocation
			// covers many processes.
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			pid, err := strconv.Atoi(fields[1])
			if err != nil {
				continue
			}
			portInfo := parsePortInfo(line)
			if portInfo != nil {
				ports[pid] = append(ports[pid], *portInfo)
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
