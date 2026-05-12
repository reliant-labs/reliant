// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

// BackgroundProcessInfo holds process information returned by the provider.
// This is a service-layer type that decouples from shell.BackgroundProcess.
type BackgroundProcessInfo struct {
	ID         string
	Command    string
	Status     string // "running", "completed", "failed", "killed", "killed_externally", "stale"
	StartTime  time.Time
	EndTime    *time.Time
	ExitCode   *int
	WorkingDir string
	WorktreeID string
	SessionID  string
	ChatID     string
	Ports      []PortInfo
}

// PortInfo holds information about a port used by a process.
type PortInfo struct {
	Port     int
	Protocol string
	State    string
	Address  string
}

// BackgroundProcessOutput holds stdout/stderr output for a process.
type BackgroundProcessOutput struct {
	Stdout string
	Stderr string
}

// OutputSubscriptionInfo wraps the shell output subscription for streaming.
type OutputSubscriptionInfo struct {
	Sub *shell.OutputSubscription
}

// BackgroundProcessProvider abstracts access to background process state.
// Implementations:
//   - DBBackgroundProcessProvider: queries background_processes DB table (distributed api-server)
type BackgroundProcessProvider interface {
	// ListProcesses returns processes matching the given filters.
	ListProcesses(ctx context.Context, worktreeID, sessionID, chatID string) ([]BackgroundProcessInfo, error)

	// GetProcess returns a single process by ID.
	GetProcess(ctx context.Context, processID string) (*BackgroundProcessInfo, error)

	// GetOutput returns stdout/stderr for a process.
	GetOutput(ctx context.Context, processID string) (*BackgroundProcessOutput, error)

	// KillProcess terminates a running process.
	KillProcess(ctx context.Context, processID string) error

	// GetProcessStatus returns the process status, completion state, and exit code.
	GetProcessStatus(ctx context.Context, processID string) (status string, isComplete bool, exitCode *int, err error)

	// GetCombinedOutputWithSeq retrieves interleaved output with sequence numbers.
	// If afterSeq > 0, only returns lines with sequence > afterSeq.
	GetCombinedOutputWithSeq(ctx context.Context, processID string, afterSeq int64) ([]shell.OutputLineWithSeq, int64, error)

	// SupportsStreaming returns whether this provider supports real-time output streaming.
	SupportsStreaming() bool

	// SubscribeToOutput subscribes to real-time output (only supported by LocalProvider).
	// Returns an error if streaming is not supported.
	SubscribeToOutput(ctx context.Context, processID string) (*OutputSubscriptionInfo, error)

	// UnsubscribeFromOutput removes an output subscription.
	UnsubscribeFromOutput(sub *OutputSubscriptionInfo)
}
