// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"

	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

// LocalBackgroundProcessProvider wraps shell.GetBackgroundManager() for monolith/daemon mode.
// Processes live in-memory on the same machine that runs the BackgroundService.
type LocalBackgroundProcessProvider struct{}

// NewLocalBackgroundProcessProvider creates a new local provider.
func NewLocalBackgroundProcessProvider() *LocalBackgroundProcessProvider {
	return &LocalBackgroundProcessProvider{}
}

// ListProcesses delegates to the in-memory BackgroundManager.
func (p *LocalBackgroundProcessProvider) ListProcesses(_ context.Context, worktreeID, sessionID, chatID string) ([]BackgroundProcessInfo, error) {
	bgManager := shell.GetBackgroundManager()

	var processes []*shell.BackgroundProcess
	if worktreeID != "" {
		processes = bgManager.GetProcessesByWorktree(worktreeID)
	} else if sessionID != "" {
		processes = bgManager.GetProcessesBySession(sessionID)
	} else if chatID != "" {
		processes = bgManager.GetProcessesByChat(chatID)
	} else {
		processes = bgManager.GetAllProcesses()
	}

	result := make([]BackgroundProcessInfo, len(processes))
	for i, proc := range processes {
		// Re-fetch to get up-to-date port info
		withPorts, err := bgManager.GetProcess(proc.ID)
		if err == nil && withPorts != nil {
			proc = withPorts
		}
		result[i] = shellProcessToInfo(proc)
	}
	return result, nil
}

// GetProcess returns a single process by ID from the in-memory manager.
func (p *LocalBackgroundProcessProvider) GetProcess(_ context.Context, processID string) (*BackgroundProcessInfo, error) {
	bgManager := shell.GetBackgroundManager()
	proc, err := bgManager.GetProcess(processID)
	if err != nil {
		return nil, err
	}
	info := shellProcessToInfo(proc)
	return &info, nil
}

// GetOutput returns stdout/stderr for a process.
func (p *LocalBackgroundProcessProvider) GetOutput(_ context.Context, processID string) (*BackgroundProcessOutput, error) {
	bgManager := shell.GetBackgroundManager()
	stdout, stderr, err := bgManager.GetOutput(processID)
	if err != nil {
		return nil, err
	}
	return &BackgroundProcessOutput{
		Stdout: stdout,
		Stderr: stderr,
	}, nil
}

// KillProcess terminates a running process.
func (p *LocalBackgroundProcessProvider) KillProcess(_ context.Context, processID string) error {
	bgManager := shell.GetBackgroundManager()
	return bgManager.KillProcess(processID)
}

// GetProcessStatus returns the current status and completion state of a process.
func (p *LocalBackgroundProcessProvider) GetProcessStatus(_ context.Context, processID string) (string, bool, *int, error) {
	bgManager := shell.GetBackgroundManager()
	return bgManager.GetProcessStatus(processID)
}

// GetCombinedOutputWithSeq retrieves interleaved output with sequence numbers.
func (p *LocalBackgroundProcessProvider) GetCombinedOutputWithSeq(_ context.Context, processID string, afterSeq int64) ([]shell.OutputLineWithSeq, int64, error) {
	bgManager := shell.GetBackgroundManager()
	return bgManager.GetCombinedOutputWithSeq(processID, afterSeq)
}

// SupportsStreaming returns true — the local provider supports real-time output streaming.
func (p *LocalBackgroundProcessProvider) SupportsStreaming() bool {
	return true
}

// SubscribeToOutput subscribes to real-time output from the in-memory manager.
func (p *LocalBackgroundProcessProvider) SubscribeToOutput(_ context.Context, processID string) (*OutputSubscriptionInfo, error) {
	bgManager := shell.GetBackgroundManager()
	sub, err := bgManager.SubscribeToOutput(processID)
	if err != nil {
		return nil, err
	}
	return &OutputSubscriptionInfo{Sub: sub}, nil
}

// UnsubscribeFromOutput removes an output subscription.
func (p *LocalBackgroundProcessProvider) UnsubscribeFromOutput(sub *OutputSubscriptionInfo) {
	if sub == nil || sub.Sub == nil {
		return
	}
	bgManager := shell.GetBackgroundManager()
	bgManager.UnsubscribeFromOutput(sub.Sub)
}

// shellProcessToInfo converts a shell.BackgroundProcess to BackgroundProcessInfo.
func shellProcessToInfo(proc *shell.BackgroundProcess) BackgroundProcessInfo {
	if proc == nil {
		return BackgroundProcessInfo{}
	}
	info := BackgroundProcessInfo{
		ID:         proc.ID,
		Command:    proc.Command,
		Status:     proc.Status,
		StartTime:  proc.StartTime,
		EndTime:    proc.EndTime,
		ExitCode:   proc.ExitCode,
		WorkingDir: proc.WorkingDir,
		WorktreeID: proc.WorktreeID,
		SessionID:  proc.SessionID,
		ChatID:     proc.ChatID,
	}
	if len(proc.Ports) > 0 {
		info.Ports = make([]PortInfo, len(proc.Ports))
		for i, port := range proc.Ports {
			info.Ports[i] = PortInfo{
				Port:     port.Port,
				Protocol: port.Protocol,
				State:    port.State,
				Address:  port.Address,
			}
		}
	}
	return info
}

// Compile-time interface check.
var _ BackgroundProcessProvider = (*LocalBackgroundProcessProvider)(nil)
