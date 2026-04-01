// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// ShellParams defines the parameters for the unified shell tool.
// This tool executes commands using the platform's native shell:
// - Unix/macOS/Linux: bash -c
// - Windows: powershell.exe -NoProfile -Command
type ShellParams struct {
	Command         string            `json:"command" jsonschema:"required,description=The command to execute"`
	Timeout         int               `json:"timeout,omitempty" jsonschema:"description=Optional timeout in milliseconds (max 600000)"`
	RunInBackground bool              `json:"run_in_background,omitempty" jsonschema:"description=Run the command in the background and return immediately"`
	Env             map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set for this command execution"`
	MaxOutput       int               `json:"max_output,omitempty" jsonschema:"description=Maximum bytes of output to collect (default: 16000)"`
	TailLines       int               `json:"tail_lines,omitempty" jsonschema:"description=Only return last N lines of output"`
}

// ShellPermissionsParams is used for permission checking
type ShellPermissionsParams struct {
	Command         string            `json:"command"`
	Timeout         int               `json:"timeout"`
	RunInBackground bool              `json:"run_in_background"`
	Env             map[string]string `json:"env,omitempty"`
}

// ShellResponseMetadata contains metadata about the shell execution
type ShellResponseMetadata struct {
	StartTime    int64  `json:"start_time"`
	EndTime      int64  `json:"end_time"`
	ProcessID    string `json:"process_id,omitempty"` // For background processes
	OutputSize   int    `json:"output_size,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	OriginalSize int    `json:"original_size,omitempty"` // Size before truncation
}

type shellTool struct{}

// ShellToolName is defined in platform-specific files:
// - shell_name_unix.go: "bash"
// - shell_name_windows.go: "powershell"

const (
	DefaultShellTimeout  = 1 * time.Minute
	MaxShellTimeout      = 10 * time.Minute
	MaxShellOutputLength = 16000
)

// bannedShellCommands are commands that are not allowed to be executed
var bannedShellCommands = []string{
	"alias", "axel", "aria2c",
	"w3m", "links", "xh",
}

// safeReadOnlyShellCommands are commands that don't require permission
var safeReadOnlyShellCommands = []string{
	// Common Unix commands
	"ls", "echo", "pwd", "date", "cal", "uptime", "whoami", "id", "groups", "env", "printenv", "set", "unset", "which", "type", "whereis",
	"whatis", "uname", "hostname", "df", "du", "free", "top", "ps", "kill", "killall", "nice", "nohup", "time", "timeout",
	// Git commands
	"git status", "git log", "git diff", "git show", "git branch", "git tag", "git remote", "git ls-files", "git ls-remote",
	"git rev-parse", "git config --get", "git config --list", "git describe", "git blame", "git grep", "git shortlog",
	// Go commands
	"go version", "go help", "go list", "go env", "go doc", "go vet", "go fmt", "go mod", "go test", "go build", "go run", "go install", "go clean",
	// PowerShell read-only commands (for Windows compatibility)
	"Get-ChildItem", "Get-Content", "Get-Location", "Get-Date", "Get-Host", "Get-Command", "Get-Help",
	"Get-Process", "Get-Service", "Test-Path", "Select-String", "Where-Object", "Select-Object",
}

// shellDescription is defined in platform-specific files:
// - shell_name_unix.go: bash-specific description
// - shell_name_windows.go: powershell-specific description

func shellDescriptionCommon() string {
	return `
# ❌ WHEN NOT TO USE THIS TOOL
- File editing → Use Edit/Write tools
- File reading → Use View tool
- Searching files → Use Grep/Glob tools
- Directory listing → Use LS tool

# Output Processing
- Default output limit is 16000 bytes (use max_output to customize)
- Use tail_lines to get only the last N lines of output
- Output metadata includes truncation info and original size

Usage notes:
- The command argument is required.
- You can specify an optional timeout in milliseconds (up to 600000ms / 10 minutes). If not specified, commands will timeout after 60 seconds.
- Use 'run_in_background: true' to run long-running commands in the background. You can then use BashOutput to check output, BashKill to terminate, and BashList to see all running processes.
- VERY IMPORTANT: You MUST avoid using shell in favor of other tools whenever possible, ie: commands like 'find' and 'grep'. Instead use Grep, Glob, or Agent tools to search. You MUST avoid read tools like 'cat', 'head', 'tail', and 'ls', and use FileRead and LS tools to read files.
- VERY IMPORTANT: YOU MUST AVOID WRITING FILES USING SHELL. Please use the appropriate edit and create tools.
- When issuing multiple commands, use the ';' or '&&' operator to separate them. DO NOT use newlines (newlines are ok in quoted strings).

STATELESS EXECUTION:
- IMPORTANT: Each command runs in a fresh, stateless shell. Environment variables and directory changes from previous commands do NOT persist.
- **IMPORTANT**: The current working directory is ALWAYS automatically set to the current worktree. There is NO need to cd to the worktree before running commands - just run them directly.
- To change to a subdirectory within the worktree, use 'cd' as part of a compound command (e.g., 'cd subdir && npm test').
- Environment variables set in prior shell sessions will NOT be included. Use the 'env' parameter to set environment variables for a specific command execution.
- If you need to maintain state across commands (e.g., activating a virtual environment), combine commands with && or ; operators.
- Background processes run in separate shell instances and also start fresh without inherited state.
<good-example>
pytest /foo/bar/tests
</good-example>
<bad-example>
cd /foo/bar && pytest tests
</bad-example>

Important:
- Return an empty response - the user will see the output directly
- Never update git config`
}

func NewShellTool() Tool {
	tool := &shellTool{}
	return NewToolWrapper(tool)
}

func (s *shellTool) Name() string {
	return shellToolName()
}

func (s *shellTool) Description() string {
	return shellDescription()
}

func (s *shellTool) RequiresPermission(params ShellParams) (bool, error) {
	if params.Command == "" {
		return false, fmt.Errorf("missing command")
	}

	// Check if command is banned
	baseCmd := strings.Fields(params.Command)[0]
	for _, banned := range bannedShellCommands {
		if strings.EqualFold(baseCmd, banned) {
			return false, fmt.Errorf("command '%s' is not allowed", baseCmd)
		}
	}

	// Check if command is safe read-only - if so, no permission needed
	isSafeReadOnly := false
	cmdLower := strings.ToLower(params.Command)

	for _, safe := range safeReadOnlyShellCommands {
		if strings.HasPrefix(cmdLower, strings.ToLower(safe)) {
			if len(cmdLower) == len(safe) || cmdLower[len(safe)] == ' ' || cmdLower[len(safe)] == '-' {
				isSafeReadOnly = true
				break
			}
		}
	}

	if isSafeReadOnly {
		return false, nil // No permission needed for safe read-only commands
	}

	return true, nil // Permission needed for potentially dangerous commands
}

func (s *shellTool) Execute(rctx *rctx.ToolContext, params ShellParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("command execution requires a connected daemon"), nil
	}

	// params.Timeout is in milliseconds per the JSON schema
	maxTimeoutMs := int(MaxShellTimeout.Milliseconds())
	defaultTimeoutMs := int(DefaultShellTimeout.Milliseconds())

	if params.Timeout > maxTimeoutMs {
		params.Timeout = maxTimeoutMs
	} else if params.Timeout <= 0 {
		params.Timeout = defaultTimeoutMs
	}

	chatID := rctx.ChatID
	if chatID == "" {
		return ToolResponse{}, fmt.Errorf("chat ID is required")
	}

	workingDir, err := GetWorkingDirectory(rctx)
	if err != nil {
		return NewTextErrorResponse("No project working directory available - ensure you're working within a project"), nil
	}
	startTime := time.Now()

	// Best-effort detection: if command ends with & (not in quotes), treat as background
	if !params.RunInBackground && shouldRunInBackground(params.Command) {
		params.RunInBackground = true
		params.Command = strings.TrimSuffix(strings.TrimSpace(params.Command), "&")
		params.Command = strings.TrimSpace(params.Command)
		logging.Debug("[Shell] Detected trailing &, converting to background execution", "command", params.Command)
	}

	// Build the daemon request
	req := &daemon.RunCommandRequest{
		Command:    params.Command,
		WorkingDir: workingDir,
		Env:        params.Env,
		TimeoutMs:  params.Timeout,
	}

	// Handle background execution
	if params.RunInBackground {
		processID, err := rctx.Daemon.StartBackground(rctx.Context, req)
		if err != nil {
			return ToolResponse{}, fmt.Errorf("failed to start background process: %w", err)
		}

		metadata := ShellResponseMetadata{
			StartTime: startTime.UnixMilli(),
			EndTime:   time.Now().UnixMilli(),
			ProcessID: processID,
		}

		responseText := fmt.Sprintf("Started background process with ID: %s\nCommand: %s\nUse BashOutput tool to get output, BashKill to terminate",
			processID, params.Command)
		response := WithResponseMetadata(NewTextResponse(responseText), metadata)
		response.Backgrounded = true
		return response, nil
	}

	// Foreground execution via daemon
	result, err := rctx.Daemon.RunCommand(rctx.Context, req)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("command execution failed: %w", err)
	}

	stdout := result.Stdout
	stderr := result.Stderr
	exitCode := result.ExitCode
	interrupted := result.TimedOut

	// Apply custom output limits if specified
	maxOutput := MaxShellOutputLength
	if params.MaxOutput > 0 {
		maxOutput = params.MaxOutput
	}

	originalStdoutSize := len(stdout)
	originalStderrSize := len(stderr)

	// Apply tail lines if specified
	if params.TailLines > 0 {
		stdout = getTailLines(stdout, params.TailLines)
		stderr = getTailLines(stderr, params.TailLines)
	} else {
		// Apply max output truncation
		stdout = truncateOutputWithLimit(stdout, maxOutput)
		stderr = truncateOutputWithLimit(stderr, maxOutput/2) // Give stderr less space
	}

	errorMessage := stderr
	if interrupted {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += "Command was aborted before completion"
	} else if exitCode != 0 {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += fmt.Sprintf("Exit code %d", exitCode)
	}

	hasBothOutputs := stdout != "" && stderr != ""

	if hasBothOutputs {
		stdout += "\n"
	}

	if errorMessage != "" {
		stdout += "\n" + errorMessage
	}

	wasTruncated := len(stdout) < originalStdoutSize || len(stderr) < originalStderrSize

	metadata := ShellResponseMetadata{
		StartTime:    startTime.UnixMilli(),
		EndTime:      time.Now().UnixMilli(),
		OutputSize:   len(stdout) + len(stderr),
		Truncated:    wasTruncated,
		OriginalSize: originalStdoutSize + originalStderrSize,
	}

	if stdout == "" {
		return WithResponseMetadata(NewTextResponse("no output"), metadata), nil
	}
	return WithResponseMetadata(NewTextResponse(stdout), metadata), nil
}

// shellCommandResult holds the result of a shell command execution
type shellCommandResult struct {
	Stdout       string
	Stderr       string
	ExitCode     int
	Interrupted  bool
	Backgrounded bool
	ProcessID    string
}

// executeShellCommand is implemented in platform-specific files:
// - shell_unix.go: uses bash -c
// - shell_windows.go: uses powershell.exe -NoProfile -Command

// getToolCallID extracts the tool call ID from the context for background signal handling
func getToolCallID(rctx *rctx.ToolContext) string {
	// Preserved for background signal handling but currently unused
	// with daemon-based execution
	return ""
}

// formatShellEnvVars converts a map of environment variables to a slice of "KEY=VALUE" strings
func formatShellEnvVars(env map[string]string) []string {
	var result []string
	for key, value := range env {
		result = append(result, fmt.Sprintf("%s=%s", key, value))
	}
	return result
}

// getShellExitCode extracts the exit code from an error
func getShellExitCode(err error) int {
	if err == nil {
		return 0
	}
	// Try to get exit code from exec.ExitError
	if exitErr, ok := err.(interface{ ExitCode() int }); ok {
		return exitErr.ExitCode()
	}
	return -1
}

// logShellInfo logs info for shell execution
func logShellInfo(format string, args ...interface{}) {
	logging.Info(fmt.Sprintf("[Shell] "+format, args...))
}

// logShellError logs errors for shell execution
func logShellError(format string, args ...interface{}) {
	logging.Error(fmt.Sprintf("[Shell] "+format, args...))
}

// countShellLines counts the number of lines in a string
func countShellLines(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// truncateOutputWithLimit truncates output, showing head and tail with a summary in between
func truncateOutputWithLimit(content string, limit int) string {
	if len(content) <= limit {
		return content
	}

	halfLength := limit / 2
	start := content[:halfLength]
	end := content[len(content)-halfLength:]

	truncatedLinesCount := countShellLines(content[halfLength : len(content)-halfLength])
	return fmt.Sprintf("%s\n\n... [%d lines truncated] ...\n\n%s", start, truncatedLinesCount, end)
}

// getTailLines returns the last n lines of content
func getTailLines(content string, n int) string {
	if content == "" {
		return content
	}

	// Handle trailing newline properly
	endsWithNewline := strings.HasSuffix(content, "\n")
	lines := strings.Split(content, "\n")

	// If content ends with newline, the split creates an empty string at the end
	if endsWithNewline && len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if len(lines) <= n {
		return content
	}

	result := strings.Join(lines[len(lines)-n:], "\n")
	if endsWithNewline {
		result += "\n"
	}
	return result
}

// shouldRunInBackground detects if a command should run in the background.
// This is a best-effort heuristic that checks for trailing & not inside quotes.
// Complex shell constructs may not be detected correctly.
func shouldRunInBackground(command string) bool {
	cmd := strings.TrimSpace(command)
	if !strings.HasSuffix(cmd, "&") {
		return false
	}

	// Check if the & is escaped or inside quotes
	// Simple heuristic: count unescaped quotes to see if we're inside a string
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for i := 0; i < len(cmd)-1; i++ { // -1 to exclude the trailing &
		c := cmd[i]

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' {
			escaped = true
			continue
		}

		if c == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
		} else if c == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
		}
	}

	// If we're inside quotes at the end, the & is part of a string argument
	if inSingleQuote || inDoubleQuote {
		return false
	}

	// Check if the & is escaped (preceded by \)
	if len(cmd) >= 2 && cmd[len(cmd)-2] == '\\' {
		return false
	}

	return true
}
