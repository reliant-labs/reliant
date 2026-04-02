// Copyright (c) 2025 Reliant Labs
//go:build !windows

package tools

// shellToolName returns the platform-specific tool name
func shellToolName() string {
	return "bash"
}
