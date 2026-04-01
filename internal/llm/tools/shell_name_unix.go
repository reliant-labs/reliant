// Copyright (c) 2025 Reliant Labs
//go:build !windows

package tools

// ShellToolName is "bash" on Unix/macOS/Linux systems.
// This allows workflows to use tag:shell to get the appropriate shell tool.
const ShellToolName = "bash"

func shellDescription() string {
	return `Execute bash commands for building, testing, and system operations in a stateless shell.

Uses bash -c to execute commands on Unix/macOS/Linux.` + shellDescriptionCommon()
}
