// Copyright (c) 2025 Reliant Labs
//go:build !windows

package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// shellToolName returns the platform-specific tool name
func shellToolName() string {
	return "bash"
}

// executeShellCommand runs a command using bash on Unix/macOS/Linux
func executeShellCommand(rctx *rctx.ToolContext, command string, workingDir string, timeoutMs int, env map[string]string) shellCommandResult {
	// Create a context with timeout
	timeoutDuration := time.Duration(timeoutMs) * time.Millisecond
	ctx, cancel := rctx.WithTimeout(timeoutDuration)
	defer cancel()

	// Get toolCallID for background signal handling
	toolCallID := getToolCallID(rctx)

	// Use bash -c to execute the command
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = workingDir

	// Put child in its own process group so that if it tries to read from the
	// controlling terminal (e.g. ssh password prompts) it doesn't send SIGTTIN
	// to the backend's entire process group, which would stop the server.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Set up environment variables
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), formatShellEnvVars(env)...)
	}

	// Capture output
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	startTime := time.Now()

	// Start the command
	if err := cmd.Start(); err != nil {
		return shellCommandResult{
			Stdout:      "",
			Stderr:      fmt.Sprintf("Failed to start command: %v", err),
			ExitCode:    1,
			Interrupted: false,
		}
	}

	// Wait for command completion in a goroutine
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	// Poll for completion or background signal
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			// Command completed normally
			stdout := stdoutBuf.String()
			stderr := stderrBuf.String()
			exitCode := 0
			interrupted := false

			if err != nil {
				exitCode = getShellExitCode(err)
				// Check if context was cancelled (timeout or interruption)
				if ctx.Err() == context.DeadlineExceeded || ctx.Err() == context.Canceled {
					interrupted = true
				}
			}

			// Check for background signal before clearing
			// The user may have clicked "Run in Background" right before the command finished.
			if toolCallID != "" && shell.GetBackgroundSignal().IsBackgrounded(toolCallID) {
				logShellInfo("Command completed but background signal was set - treating as backgrounded",
					"toolCallID", toolCallID,
					"command", command)

				// Clear the signal
				shell.GetBackgroundSignal().ClearBackgrounded(toolCallID)

				// Return as "backgrounded" with the completed output
				response := fmt.Sprintf("Command completed (was marked for background).\nOutput:\n%s", stdout)
				if stderr != "" {
					response += fmt.Sprintf("\nStderr:\n%s", stderr)
				}

				return shellCommandResult{
					Stdout:       response,
					Stderr:       "",
					ExitCode:     exitCode,
					Interrupted:  false,
					Backgrounded: true,
				}
			}

			// Clear any background signal since command completed normally
			if toolCallID != "" {
				shell.GetBackgroundSignal().ClearBackgrounded(toolCallID)
			}

			return shellCommandResult{
				Stdout:      stdout,
				Stderr:      stderr,
				ExitCode:    exitCode,
				Interrupted: interrupted,
			}

		case <-ticker.C:
			// Check for background signal
			if toolCallID != "" && shell.GetBackgroundSignal().IsBackgrounded(toolCallID) {
				logShellInfo("Detected background signal, converting to background process",
					"toolCallID", toolCallID,
					"command", command)

				// Clear the signal
				shell.GetBackgroundSignal().ClearBackgrounded(toolCallID)

				// Get worktree and session info from context
				worktreeID := ""
				if rctx.Worktree != nil {
					worktreeID = rctx.Worktree.ID
				}
				chatID := rctx.ChatID

				// Adopt the running process into the background manager
				process, adoptErr := shell.GetBackgroundManager().AdoptRunningProcess(shell.AdoptRunningProcessOptions{
					Cmd:        cmd,
					Command:    command,
					WorkingDir: workingDir,
					WorktreeID: worktreeID,
					SessionID:  chatID,
					ChatID:     chatID,
					StartTime:  startTime,
					StdoutBuf:  &stdoutBuf,
					StderrBuf:  &stderrBuf,
					WaitErrCh:  done,
				})

				if adoptErr != nil {
					logShellError("Failed to adopt process to background",
						"error", adoptErr,
						"toolCallID", toolCallID)

					// Fall back to waiting for completion
					err := <-done
					exitCode := 0
					if err != nil {
						exitCode = getShellExitCode(err)
					}
					return shellCommandResult{
						Stdout:   stdoutBuf.String(),
						Stderr:   stderrBuf.String() + "\n(Failed to convert to background: " + adoptErr.Error() + ")",
						ExitCode: exitCode,
					}
				}

				logShellInfo("Successfully converted to background process",
					"processID", process.ID,
					"toolCallID", toolCallID)

				// Return with backgrounded response
				response := fmt.Sprintf("Command converted to background process.\nProcess ID: %s\nCommand: %s\nUse BashOutput tool to get output, BashKill to terminate.",
					process.ID, command)

				return shellCommandResult{
					Stdout:       response,
					Stderr:       "",
					ExitCode:     0,
					Interrupted:  false,
					Backgrounded: true,
					ProcessID:    process.ID,
				}
			}

		case <-ctx.Done():
			// Context cancelled (timeout or cancellation)
			if toolCallID != "" {
				shell.GetBackgroundSignal().ClearBackgrounded(toolCallID)
			}
			return shellCommandResult{
				Stdout:      stdoutBuf.String(),
				Stderr:      stderrBuf.String(),
				ExitCode:    getShellExitCode(ctx.Err()),
				Interrupted: true,
			}
		}
	}
}
