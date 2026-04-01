//go:build windows

package daemon

import (
	"context"
	"os/exec"
)

// createShellCmd creates a shell command for Windows.
func createShellCmd(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", command)
}
