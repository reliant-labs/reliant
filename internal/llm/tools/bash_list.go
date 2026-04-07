// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/rctx"
)

type BashListParams struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"description=Filter by session ID (optional)"`
	ChatID    string `json:"chat_id,omitempty" jsonschema:"description=Filter by chat ID (optional)"`
	All       bool   `json:"all,omitempty" jsonschema:"description=Show all processes including completed ones (default: false - show only running)"`
	// WorktreeID is set implicitly from the tool context - not exposed to users
	WorktreeID string `json:"-"`
}

type ProcessInfo struct {
	ID         string     `json:"id"`
	Command    string     `json:"command"`
	Status     string     `json:"status"`
	StartTime  time.Time  `json:"start_time"`
	EndTime    *time.Time `json:"end_time,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	WorkingDir string     `json:"working_dir"`
	SessionID  string     `json:"session_id"`
	ChatID     string     `json:"chat_id,omitempty"`
}

type BashListResponseMetadata struct {
	TotalProcesses   int `json:"total_processes"`
	RunningProcesses int `json:"running_processes"`
}

type bashListTool struct{}

const (
	BashListToolName    = "bash_list"
	bashListDescription = `Lists background processes in the current workspace.

WORKSPACE SCOPING:
- Processes are scoped to the current workspace (worktree)
- Multiple chats in the same workspace share the same process list
- This enables coordination: one chat can start a server, another can check its status
- Use BashOutput to view output and BashKill to terminate any workspace process

Usage notes:
- By default, shows only running processes in the current workspace
- Use 'all: true' to include completed, failed, and killed processes
- Process IDs can be used with BashOutput and BashKill tools

Example outputs:
- Running processes: Shows ID, command, and how long they've been running
- Completed processes: Shows ID, command, exit code, and duration
- Failed processes: Shows ID, command, exit code, and error indication

Examples:
1. List running processes: bash_list()
2. List all processes including completed: bash_list(all=true)`
)

func NewBashListTool() Tool {
	tool := &bashListTool{}
	return NewToolWrapper[BashListParams, ToolResponse](tool)
}

func (b *bashListTool) Name() string {
	return BashListToolName
}

func (b *bashListTool) Description() string {
	return bashListDescription
}

func (b *bashListTool) RequiresPermission(params BashListParams) (bool, error) {
	// bash_list tool doesn't require permissions as it's read-only
	return false, nil
}

func (b *bashListTool) Execute(rctx *rctx.ToolContext, params BashListParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("command execution requires a connected daemon"), nil
	}

	processes, err := rctx.Daemon.ListProcesses(rctx.Context)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list processes: %v", err)), nil
	}

	// Filter out non-running processes if not showing all
	var filteredProcesses []*filteredProcess
	runningCount := 0
	for _, p := range processes {
		if p.Status == "running" {
			runningCount++
		}
		if params.All || p.Status == "running" {
			filteredProcesses = append(filteredProcesses, &filteredProcess{
				ID:        p.ID,
				Command:   p.Command,
				Status:    p.Status,
				StartTime: p.StartTime,
				EndTime:   p.EndTime,
				ExitCode:  p.ExitCode,
			})
		}
	}

	// Format the output
	if len(filteredProcesses) == 0 {
		msg := "No background processes"
		if !params.All {
			msg += " running"
		}
		return NewTextResponse(msg), nil
	}

	var output strings.Builder
	output.WriteString("=== Background Processes ===\n\n")

	for i, p := range filteredProcesses {
		if i > 0 {
			output.WriteString("\n---\n\n")
		}

		fmt.Fprintf(&output, "Process ID: %s\n", p.ID)
		fmt.Fprintf(&output, "Status: %s\n", p.Status)
		fmt.Fprintf(&output, "Command: %s\n", p.Command)
		fmt.Fprintf(&output, "Started: %s\n", p.StartTime.Format(time.RFC3339))

		if p.Status == "running" {
			duration := time.Since(p.StartTime)
			fmt.Fprintf(&output, "Running for: %s\n", formatDuration(duration))
		} else if p.EndTime != nil {
			fmt.Fprintf(&output, "Ended: %s\n", p.EndTime.Format(time.RFC3339))
			duration := p.EndTime.Sub(p.StartTime)
			fmt.Fprintf(&output, "Duration: %s\n", formatDuration(duration))
		}

		if p.ExitCode != nil {
			fmt.Fprintf(&output, "Exit Code: %d\n", *p.ExitCode)
		}
	}

	fmt.Fprintf(&output, "\n\nTotal: %d processes (%d running)\n", len(filteredProcesses), runningCount)

	metadata := BashListResponseMetadata{
		TotalProcesses:   len(filteredProcesses),
		RunningProcesses: runningCount,
	}

	return WithResponseMetadata(NewTextResponse(output.String()), metadata), nil
}

// filteredProcess is a local type for formatting daemon process info
type filteredProcess struct {
	ID        string
	Command   string
	Status    string
	StartTime time.Time
	EndTime   *time.Time
	ExitCode  *int
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0f seconds", d.Seconds())
	} else if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := int(d.Seconds()) % 60
		return fmt.Sprintf("%d minutes, %d seconds", minutes, seconds)
	} else {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		return fmt.Sprintf("%d hours, %d minutes", hours, minutes)
	}
}
