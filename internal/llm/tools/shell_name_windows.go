// Copyright (c) 2025 Reliant Labs
//go:build windows

package tools

// ShellToolName is "powershell" on Windows systems.
// This allows workflows to use tag:shell to get the appropriate shell tool.
const ShellToolName = "powershell"

func shellDescription() string {
	return `Execute PowerShell commands for building, testing, and system operations in a stateless shell.

Uses powershell.exe -NoProfile -Command to execute commands on Windows.` + shellDescriptionCommon()
}
