// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// DBBackgroundProcessProvider queries the background_processes DB table.
// Used in distributed mode where the api-server doesn't hold process state in memory.
type DBBackgroundProcessProvider struct {
	database db.Repository
	router   toolexec.DaemonRouter
}

// NewDBBackgroundProcessProvider creates a new DB-backed provider.
func NewDBBackgroundProcessProvider(database db.Repository, router toolexec.DaemonRouter) *DBBackgroundProcessProvider {
	return &DBBackgroundProcessProvider{database: database, router: router}
}

// ListProcesses queries the DB for background processes owned by userID.
func (p *DBBackgroundProcessProvider) ListProcesses(ctx context.Context, userID, worktreeID, sessionID, chatID string) ([]BackgroundProcessInfo, error) {
	// Scope every listing to the owner. The background_processes table is
	// multi-tenant; without this filter a client could enumerate every user's
	// processes by omitting the worktree/chat filters.
	filters := db.BackgroundProcessFilters{UserID: userID}

	if worktreeID != "" {
		filters.WorktreeID = &worktreeID
	}
	// The DB model has no SessionID filter — skip if only sessionID is set.
	// In distributed mode, chatID is the more reliable filter.
	if chatID != "" {
		filters.ChatID = &chatID
	}

	processes, err := p.database.ListBackgroundProcesses(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("failed to list background processes: %w", err)
	}

	result := make([]BackgroundProcessInfo, len(processes))
	for i, proc := range processes {
		result[i] = dbProcessToInfo(proc)
	}
	return result, nil
}

// GetProcess returns a single process by ID from the DB.
func (p *DBBackgroundProcessProvider) GetProcess(ctx context.Context, processID string) (*BackgroundProcessInfo, error) {
	proc, err := p.database.GetBackgroundProcess(ctx, processID)
	if err != nil {
		return nil, err
	}
	info := dbProcessToInfo(proc)
	return &info, nil
}

// GetOutput returns process output reconstructed from persisted output lines.
func (p *DBBackgroundProcessProvider) GetOutput(ctx context.Context, processID string) (*BackgroundProcessOutput, error) {
	lines, err := p.database.GetBackgroundProcessOutput(ctx, processID, 0, 0)
	if err != nil {
		return nil, err
	}

	var stdout, stderr strings.Builder
	for _, line := range lines {
		switch line.Stream {
		case "stdout":
			stdout.WriteString(line.Line)
			stdout.WriteString("\n")
		case "stderr":
			stderr.WriteString(line.Line)
			stderr.WriteString("\n")
		}
	}

	return &BackgroundProcessOutput{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}, nil
}

// KillProcess sends a kill request to the daemon via the DaemonRouter.
func (p *DBBackgroundProcessProvider) KillProcess(ctx context.Context, processID string) error {
	process, err := p.database.GetBackgroundProcess(ctx, processID)
	if err != nil {
		return fmt.Errorf("process not found: %w", err)
	}

	online, onlineErr := p.router.IsDaemonOnline(ctx, process.UserID)
	if onlineErr != nil {
		// Infrastructure failure — log but attempt the kill anyway.
		logging.Warn("IsDaemonOnline returned infrastructure error, attempting kill anyway",
			"error", onlineErr, "userID", process.UserID, "processID", processID)
	} else if !online {
		return fmt.Errorf("cannot kill process — daemon is offline for user")
	}

	return p.router.SendKillProcess(ctx, process.UserID, processID)
}

// GetProcessStatus returns the process status from the DB.
func (p *DBBackgroundProcessProvider) GetProcessStatus(ctx context.Context, processID string) (string, bool, *int, error) {
	proc, err := p.database.GetBackgroundProcess(ctx, processID)
	if err != nil {
		return "", false, nil, err
	}

	status := db.BgProcessStatusToString(proc.Status)
	isComplete := proc.Status != db.BgProcessStatusRunning

	return status, isComplete, proc.ExitCode, nil
}

// GetCombinedOutputWithSeq returns interleaved output lines with sequence numbers from the DB.
func (p *DBBackgroundProcessProvider) GetCombinedOutputWithSeq(ctx context.Context, processID string, afterSeq int64) ([]shell.OutputLineWithSeq, int64, error) {
	lines, err := p.database.GetBackgroundProcessOutput(ctx, processID, afterSeq, 0)
	if err != nil {
		return nil, 0, err
	}

	result := make([]shell.OutputLineWithSeq, len(lines))
	var maxSeq int64
	for i, line := range lines {
		result[i] = shell.OutputLineWithSeq{
			Type:     line.Stream,
			Text:     line.Line,
			Sequence: line.Seq,
		}
		if line.Seq > maxSeq {
			maxSeq = line.Seq
		}
	}

	return result, maxSeq, nil
}

// SupportsStreaming returns false — the DB provider cannot stream real-time output.
func (p *DBBackgroundProcessProvider) SupportsStreaming() bool {
	return false
}

// SubscribeToOutput is not supported for DB-backed processes.
func (p *DBBackgroundProcessProvider) SubscribeToOutput(_ context.Context, _ string) (*OutputSubscriptionInfo, error) {
	return nil, fmt.Errorf("real-time output streaming not supported for remote processes")
}

// UnsubscribeFromOutput is a no-op for the DB provider.
func (p *DBBackgroundProcessProvider) UnsubscribeFromOutput(_ *OutputSubscriptionInfo) {}

// dbProcessToInfo converts a db.BackgroundProcess to BackgroundProcessInfo.
func dbProcessToInfo(proc *db.BackgroundProcess) BackgroundProcessInfo {
	if proc == nil {
		return BackgroundProcessInfo{}
	}
	info := BackgroundProcessInfo{
		ID:         proc.ID,
		Command:    proc.Command,
		Status:     db.BgProcessStatusToString(proc.Status),
		StartTime:  proc.StartedAt,
		EndTime:    proc.EndedAt,
		ExitCode:   proc.ExitCode,
		WorkingDir: proc.WorkingDir,
		UserID:     proc.UserID,
	}
	if proc.WorktreeID != nil {
		info.WorktreeID = *proc.WorktreeID
	}
	if proc.ChatID != nil {
		info.ChatID = *proc.ChatID
	}
	// DB model has no SessionID or Ports — those are in-memory only.
	return info
}

// Compile-time interface check.
var _ BackgroundProcessProvider = (*DBBackgroundProcessProvider)(nil)
