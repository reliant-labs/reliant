// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type ShellListParams struct {
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

type ShellListResponseMetadata struct {
	TotalProcesses   int `json:"total_processes"`
	RunningProcesses int `json:"running_processes"`
}

type shellListTool struct {
	// clock reads the time idle durations are measured to; nil is time.Now.
	clock func() time.Time
}

const (
	ShellListToolName    = "shell_list"
	shellListDescription = `Lists background processes in the current workspace.

WORKSPACE SCOPING:
- Processes are scoped to the current workspace (worktree)
- Multiple chats in the same workspace share the same process list
- This enables coordination: one chat can start a server, another can check its status
- Use shell_output to view output and shell_kill to terminate any workspace process

AUTOMATIC STOP:
- A background process is stopped once it has been idle (no output, and no agent used it with shell_output, shell_wait, shell_list or shell_kill) for 4 hours by default and the chat that started it has no run in progress; one started with keep_alive: true is exempt
- Each running process shows how long it has been idle and when it could be stopped. Listing a process counts as using it

Usage notes:
- By default, shows only running processes in the current workspace
- Use 'all: true' to include completed, failed, and killed processes
- Process IDs can be used with shell_output and shell_kill tools

Example outputs:
- Running processes: Shows ID, command, and how long they've been running
- Completed processes: Shows ID, command, exit code, and duration
- Failed processes: Shows ID, command, exit code, and error indication

Examples:
1. List running processes: shell_list()
2. List all processes including completed: shell_list(all=true)`
)

func NewShellListTool() Tool {
	tool := &shellListTool{}
	return NewToolWrapper[ShellListParams, ToolResponse](tool)
}

func (b *shellListTool) Name() string {
	return ShellListToolName
}

func (b *shellListTool) Description() string {
	return shellListDescription
}

func (b *shellListTool) RequiresPermission(params ShellListParams) (bool, error) {
	// shell_list tool doesn't require permissions as it's read-only
	return false, nil
}

func (b *shellListTool) Execute(rctx *rctx.ToolContext, params ShellListParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("command execution requires a connected daemon"), nil
	}

	processes, err := rctx.Daemon.ListProcesses(rctx.Context)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list processes: %v", err)), nil
	}

	// The current workspace's processes only, as the description says. It
	// matters beyond tidiness: listing a process is an agent using it, which
	// resets its idle time, so an unscoped list in any chat on the machine
	// would keep every other workspace's leftovers alive forever.
	workspace, _ := GetWorkingDirectory(rctx)
	now := time.Now()
	if b.clock != nil {
		now = b.clock()
	}

	// Filter out non-running processes if not showing all
	var filteredProcesses []*filteredProcess
	var touched []string
	runningCount := 0
	for _, p := range processes {
		if !inWorkspace(workspace, p.WorkingDir) {
			continue
		}
		if p.Status == "running" {
			runningCount++
			touched = append(touched, p.ID)
		}
		if params.All || p.Status == "running" {
			filteredProcesses = append(filteredProcesses, &filteredProcess{
				ID:          p.ID,
				Command:     p.Command,
				Status:      p.Status,
				StartTime:   p.StartTime,
				EndTime:     p.EndTime,
				ExitCode:    p.ExitCode,
				PreviewURLs: previewURLsForProcess(p),
				AutoStop:    autoStopLines(p, now),
			})
		}
	}
	touchProcesses(rctx, touched...)

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
			duration := now.Sub(p.StartTime)
			fmt.Fprintf(&output, "Running for: %s\n", formatDuration(duration))
			// Surface the env-aware proxied preview URL for any publicly-bound
			// (0.0.0.0) listening port so the agent can post a reachable link
			// instead of the daemon's unreachable loopback.
			for _, line := range p.PreviewURLs {
				fmt.Fprintf(&output, "%s\n", line)
			}
			for _, line := range p.AutoStop {
				fmt.Fprintf(&output, "%s\n", line)
			}
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

	metadata := ShellListResponseMetadata{
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
	// PreviewURLs holds one ready-to-post line per publicly-bound listening
	// port (env-aware proxied URL), populated for running processes only.
	PreviewURLs []string
	// AutoStop says how idle a running process is and when the daemon's
	// idle reaper would stop it (autoStopLines).
	AutoStop []string
}

// autoStopLines describes how the daemon's idle reaper sees a running
// process: how long it has been idle, and when it would be stopped. Nothing
// for a finished process, or from a daemon too old to say.
//
// Listing it counts as using it, so the earliest stop is a full window from
// now, not from when it last did anything.
func autoStopLines(p *daemon.ProcessInfo, now time.Time) []string {
	if p.Status != "running" || p.LastActive.IsZero() {
		return nil
	}
	lines := []string{fmt.Sprintf("Idle for: %s (no output and no agent use)", formatDuration(now.Sub(p.LastActive)))}
	switch {
	case p.StopsAfter != nil:
		window := p.StopsAfter.Sub(p.LastActive)
		line := fmt.Sprintf("Auto-stop: after %s idle once its chat has no run in progress; not before %s (this listing counts as use)",
			formatDuration(window), now.Add(window).UTC().Format(time.RFC3339))
		if p.ChatRunning != nil && *p.ChatRunning {
			line += "; its chat is running now"
		}
		lines = append(lines, line)
	case p.StopExempt != "":
		lines = append(lines, "Auto-stop: never ("+p.StopExempt+")")
	}
	return lines
}

// inWorkspace reports whether a process running in dir belongs to the
// workspace rooted at root. Unknown on either side (an older daemon that does
// not report the directory) includes it.
func inWorkspace(root, dir string) bool {
	if root == "" || dir == "" {
		return true
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// touchProcesses tells the daemon an agent used these processes, resetting
// their idle time. Best effort: a daemon too old to know the command, or one
// that does not answer, costs the touch and nothing else.
func touchProcesses(rctx *rctx.ToolContext, processIDs ...string) {
	if rctx.Daemon == nil || len(processIDs) == 0 {
		return
	}
	if err := rctx.Daemon.TouchProcesses(rctx.Context, processIDs); err != nil {
		logging.DebugContext(rctx.Context, "Could not record background process use", "processes", processIDs, "error", err)
	}
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
