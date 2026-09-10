// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/rctx"
)

type ShellKillParams struct {
	ProcessID string `json:"process_id" jsonschema:"required,description=The ID of the background process to kill"`
}

type ShellKillPermissionsParams struct {
	ProcessID string `json:"process_id"`
	Command   string `json:"command"`
}

type shellKillTool struct {
}

const (
	ShellKillToolName    = "shell_kill"
	shellKillDescription = `Terminates a background process in the current workspace.

WORKSPACE SCOPING:
- Can kill any process in the current workspace, regardless of which chat started it
- Multiple chats in the same workspace share process visibility
- This enables coordination: one chat can stop a server started by another

This tool sends a termination signal and gives the process time to clean up.
If it doesn't stop gracefully, it will be forcefully killed.

Usage notes:
- Process IDs are provided when you start a background process with run_in_background: true
- You can only kill processes that are currently running
- After killing a process, its output is still available via shell_output
- Use shell_list to see all running background processes in the workspace

Example:
1. Start a background process: bash(command="npm run dev", run_in_background=true)
2. Kill the process: shell_kill(process_id="<id-from-step-1>")`
)

func NewShellKillTool() Tool {
	tool := &shellKillTool{}
	return NewToolWrapper[ShellKillParams, ToolResponse](tool)
}

func (b *shellKillTool) Name() string {
	return ShellKillToolName
}

func (b *shellKillTool) Description() string {
	return shellKillDescription
}

func (b *shellKillTool) RequiresPermission(params ShellKillParams) (bool, error) {
	// shell_kill tool requires permissions as it's a write operation
	return true, nil
}

func (b *shellKillTool) Execute(rctx *rctx.ToolContext, params ShellKillParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("command execution requires a connected daemon"), nil
	}

	if params.ProcessID == "" {
		return NewTextErrorResponse("process_id is required"), nil
	}

	// Kill the process via daemon
	err := rctx.Daemon.KillProcess(rctx.Context, params.ProcessID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to kill process: %v", err)), nil
	}

	response := fmt.Sprintf("Successfully killed process %s", params.ProcessID)
	return NewTextResponse(response), nil
}
