// Copyright (c) 2025 Reliant Labs
package shell

import (
	"sort"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/osutil"
)

// ProcessMonitor monitors background processes for external changes and port updates
type ProcessMonitor struct {
	manager      *BackgroundManager
	stopChan     chan struct{}
	wg           sync.WaitGroup
	isMonitoring bool
	mu           sync.RWMutex

	// Track previous port state for each process to detect changes
	previousPorts   map[string][]PortInfo
	previousPortsMu sync.RWMutex
}

// PortInfo contains information about a port used by a process
type PortInfo struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"` // tcp, udp
	State    string `json:"state"`    // LISTEN, ESTABLISHED, etc.
	Address  string `json:"address"`  // bind address
}

// ProcessStatus contains extended process information
type ProcessStatus struct {
	PID       int        `json:"pid"`
	IsRunning bool       `json:"is_running"`
	Ports     []PortInfo `json:"ports,omitempty"`
}

var (
	processMonitor     *ProcessMonitor
	processMonitorOnce sync.Once
)

// GetProcessMonitor returns the singleton process monitor instance
func GetProcessMonitor() *ProcessMonitor {
	processMonitorOnce.Do(func() {
		processMonitor = &ProcessMonitor{
			manager:       GetBackgroundManager(),
			stopChan:      make(chan struct{}),
			previousPorts: make(map[string][]PortInfo),
		}
	})
	return processMonitor
}

// Start begins monitoring background processes
func (pm *ProcessMonitor) Start() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.isMonitoring {
		return
	}

	pm.isMonitoring = true
	pm.wg.Add(1)
	go pm.monitorLoop()
}

// Stop stops monitoring background processes
func (pm *ProcessMonitor) Stop() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if !pm.isMonitoring {
		return
	}

	close(pm.stopChan)
	pm.wg.Wait()
	pm.isMonitoring = false
	pm.stopChan = make(chan struct{})
}

// monitorLoop periodically checks the status of all running processes
func (pm *ProcessMonitor) monitorLoop() {
	defer pm.wg.Done()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pm.stopChan:
			return
		case <-ticker.C:
			pm.checkProcesses()
		}
	}
}

// checkProcesses checks all running processes for external termination and port changes
func (pm *ProcessMonitor) checkProcesses() {
	processes := pm.manager.GetAllProcesses()

	// Track which processes are still running to clean up stale port tracking
	runningProcessIDs := make(map[string]bool)

	for _, process := range processes {
		if process.Status != "running" || process.cmd == nil || process.cmd.Process == nil {
			continue
		}

		pid := process.cmd.Process.Pid
		runningProcessIDs[process.ID] = true

		if !isProcessRunning(pid) {
			// Process was killed externally
			pm.handleExternalKill(process)
			continue
		}

		// Check for port changes
		pm.checkPortChanges(process, pid)
	}

	// Clean up port tracking for processes that are no longer running
	pm.cleanupStalePorts(runningProcessIDs)
}

// checkPortChanges checks if a process's ports have changed and emits events
func (pm *ProcessMonitor) checkPortChanges(process *BackgroundProcess, pid int) {
	// Get current ports for the process
	currentPorts, err := getProcessPorts(pid)
	if err != nil {
		logging.Debug("Failed to get process ports",
			"pid", pid,
			"processID", process.ID,
			"error", err)
		return
	}

	// Get previous ports
	pm.previousPortsMu.RLock()
	previousPorts := pm.previousPorts[process.ID]
	pm.previousPortsMu.RUnlock()

	// Check if ports have changed
	if portsChanged(previousPorts, currentPorts) {
		logging.Debug("Port change detected",
			"processID", process.ID,
			"previous", len(previousPorts),
			"current", len(currentPorts))

		// Update tracked ports
		pm.previousPortsMu.Lock()
		pm.previousPorts[process.ID] = currentPorts
		pm.previousPortsMu.Unlock()

		// Emit port change event (only if there are actually ports to report)
		// We emit even when ports go from N to 0 to allow cleanup, but typically
		// we care about the case where ports become available (0 to N)
		pm.manager.EmitPortChangeEvent(process.ID, currentPorts)
	}
}

// portsChanged compares two port slices to see if they differ
func portsChanged(prev, curr []PortInfo) bool {
	if len(prev) != len(curr) {
		return true
	}

	// Sort both slices by port number for comparison
	sortedPrev := make([]PortInfo, len(prev))
	sortedCurr := make([]PortInfo, len(curr))
	copy(sortedPrev, prev)
	copy(sortedCurr, curr)

	sort.Slice(sortedPrev, func(i, j int) bool {
		return sortedPrev[i].Port < sortedPrev[j].Port
	})
	sort.Slice(sortedCurr, func(i, j int) bool {
		return sortedCurr[i].Port < sortedCurr[j].Port
	})

	for i := range sortedPrev {
		if sortedPrev[i].Port != sortedCurr[i].Port ||
			sortedPrev[i].Protocol != sortedCurr[i].Protocol {
			return true
		}
	}

	return false
}

// cleanupStalePorts removes port tracking for processes that are no longer running
func (pm *ProcessMonitor) cleanupStalePorts(runningProcessIDs map[string]bool) {
	pm.previousPortsMu.Lock()
	defer pm.previousPortsMu.Unlock()

	for processID := range pm.previousPorts {
		if !runningProcessIDs[processID] {
			delete(pm.previousPorts, processID)
		}
	}
}

// handleExternalKill handles a process that was killed externally
func (pm *ProcessMonitor) handleExternalKill(process *BackgroundProcess) {
	process.outputMu.Lock()

	endTime := time.Now()
	process.EndTime = &endTime
	process.Status = "killed_externally"

	// Try to get the exit code (might not be available if killed externally)
	if process.cmd.ProcessState != nil {
		exitCode := process.cmd.ProcessState.ExitCode()
		process.ExitCode = &exitCode
	}

	// Capture values for event before unlocking
	event := ProcessEvent{
		Type:       "killed",
		ProcessID:  process.ID,
		ChatID:     process.ChatID,
		SessionID:  process.SessionID,
		WorktreeID: process.WorktreeID,
		Command:    process.Command,
		WorkingDir: process.WorkingDir,
		Status:     process.Status,
		ExitCode:   process.ExitCode,
		StartTime:  process.StartTime,
		EndTime:    process.EndTime,
		Ports:      process.Ports,
	}

	pid := process.cmd.Process.Pid

	process.outputMu.Unlock()

	// Close the done channel if it's still open
	select {
	case <-process.done:
		// Already closed
	default:
		close(process.done)
	}

	// Persist status change to database and emit event to notify frontend
	pm.manager.persistStatusChange(process.ID, process.Status, process.ExitCode, process.EndTime)
	pm.manager.emitEvent(event)

	logging.Info("Process killed externally",
		"id", process.ID,
		"command", process.Command,
		"pid", pid)
}

// GetProcessStatus gets detailed status of a process including port usage
func (pm *ProcessMonitor) GetProcessStatus(processID string) (*ProcessStatus, error) {
	bgManager := GetBackgroundManager()
	process, err := bgManager.GetProcess(processID)
	if err != nil {
		return nil, err
	}

	if process.cmd == nil || process.cmd.Process == nil {
		return &ProcessStatus{
			PID:       0,
			IsRunning: false,
		}, nil
	}

	pid := process.cmd.Process.Pid
	isRunning := isProcessRunning(pid)

	status := &ProcessStatus{
		PID:       pid,
		IsRunning: isRunning,
	}

	return status, nil
}

// isProcessRunning checks if a process with the given PID is still running.
// Uses osutil.IsProcessRunning which is platform-aware (Unix: kill -0, Windows: OpenProcess API).
func isProcessRunning(pid int) bool {
	return osutil.IsProcessRunning(pid)
}

// GetProcessPortInfo returns port information for a process by PID
func GetProcessPortInfo(processID string) ([]PortInfo, error) {
	// This function is kept for API compatibility but should avoid circular dependencies
	// The actual port fetching logic is now in background_manager.go
	return []PortInfo{}, nil
}
