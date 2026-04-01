//go:build !windows

package daemon

import (
	"context"
	"os/exec"
	"syscall"
)

// createShellCmd creates a shell command for Unix/macOS/Linux.
func createShellCmd(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	// Put child in its own process group to prevent SIGTTIN issues.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}
