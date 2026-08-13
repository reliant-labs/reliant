// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// ShellParams defines the parameters for the unified shell tool.
// This tool executes commands using the platform's native shell:
// - Unix/macOS/Linux: bash -c
// - Windows: powershell.exe -NoProfile -Command
type ShellParams struct {
	Command string `json:"command" jsonschema:"required,description=The command to execute"`
	// Description is model-authored prose describing what Command does. It is
	// never executed, never inspected, and never affects dispatch — it exists so
	// a reader (UI, transcript, audit) can see the intent of a command without
	// parsing shell. Optional: an older transcript or a model that omits it
	// still runs, so nothing may depend on it being present.
	//
	// Its schema description is attached in JSONSchemaExtend, not in a struct
	// tag: the tag parser treats the value as a literal, so a `\n` written there
	// reaches the model as a backslash-n rather than a line break, which would
	// run the multi-line examples together into one unreadable paragraph.
	Description     string            `json:"description,omitempty"`
	Timeout         int               `json:"timeout,omitempty" jsonschema:"description=Optional timeout in milliseconds (max 600000)"`
	RunInBackground bool              `json:"run_in_background,omitempty" jsonschema:"description=Run the command in the background and return immediately"`
	Env             map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set for this command execution"`
	MaxOutput       int               `json:"max_output,omitempty" jsonschema:"description=Maximum bytes of output to collect (default: 16000)"`
	TailLines       int               `json:"tail_lines,omitempty" jsonschema:"description=Only return last N lines of output"`
	Repo            string            `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo to run the command in: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Omit in single-repo projects. The system prompt lists available repos when this matters."`
}

// shellDescriptionParamDoc is the schema description for ShellParams.Description.
// It lives here as a Go string rather than in a struct tag because it needs real
// line breaks: the jsonschema tag parser copies the tag verbatim, so a `\n`
// written in a tag arrives at the model as the two characters backslash-n and
// collapses these examples into one run-on paragraph.
const shellDescriptionParamDoc = `Clear, concise description of what this command does in active voice. Never use words like "complex" or "risk" in the description - just describe what it does.

For simple commands (git, npm, standard CLI tools), keep it brief (5-10 words):
- ls → "List files in current directory"
- git status → "Show working tree status"
- npm install → "Install package dependencies"

For commands that are harder to parse at a glance (piped commands, obscure flags, etc.), add enough context to clarify what it does:
- find . -name "*.tmp" -exec rm {} \; → "Find and delete all .tmp files recursively"
- git reset --hard origin/main → "Discard all local changes and match remote main"
- curl -s url | jq '.data[]' → "Fetch JSON from URL and extract data array elements"`

// JSONSchemaExtend attaches the multi-line description doc after the reflector
// has built the schema from the struct tags. invopop/jsonschema calls this for
// any type implementing the hook, which is what lets the prose above keep its
// line breaks.
func (ShellParams) JSONSchemaExtend(s *jsonschema.Schema) {
	if s.Properties == nil {
		return
	}
	if prop, ok := s.Properties.Get("description"); ok {
		prop.Description = shellDescriptionParamDoc
	}
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
	StartTime int64  `json:"start_time"`
	EndTime   int64  `json:"end_time"`
	ProcessID string `json:"process_id,omitempty"` // For background processes
	// CommandDurationMs is how long the command itself ran, as measured on the
	// machine that ran it. StartTime/EndTime bracket the whole tool call, so
	// the pair answers "was the command slow, or was getting to it slow?" —
	// unconditionally and for every call, because metadata is persisted but
	// never sent to a model, so recording it costs nothing that matters.
	CommandDurationMs int64 `json:"command_duration_ms,omitempty"`
	OutputSize        int   `json:"output_size,omitempty"`
	Truncated         bool  `json:"truncated,omitempty"`
	OriginalSize      int   `json:"original_size,omitempty"` // Size before truncation
}

// Thresholds for reporting a transport/command clock disagreement to the model.
// Both must be met: the absolute floor keeps sub-second scheduling jitter
// silent, and the relative floor keeps ordinary overhead silent on a long
// command, where the same absolute gap means nothing.
const (
	transportGapFloorMs  = 1000
	transportGapMinShare = 10 // gap must be at least 1/10th of the whole call
)

// bashTransportTiming returns a timing block when the wall-clock cost of the
// tool call materially exceeds the command's own runtime, and nil when the two
// clocks agree.
//
// The policy is "report the disagreement, not the number". A duration attached
// to every shell result is charged to the model's context on every call and
// re-sent on every subsequent turn, and it carries no decision for the
// overwhelming majority of calls where nothing is wrong — it is exactly the
// kind of always-present field a reader learns to skip, which is how it fails
// to be noticed on the one call that mattered. A field that appears only when
// two independently measured clocks contradict each other is self-selecting
// evidence: its presence is the finding.
func bashTransportTiming(wallMs, commandMs int64) *BashTiming {
	if commandMs <= 0 {
		// Nothing to compare against: an executor that does not measure the
		// command cannot support a claim about where the time went.
		return nil
	}
	gap := wallMs - commandMs
	if gap < transportGapFloorMs || gap*transportGapMinShare < wallMs {
		return nil
	}
	return &BashTiming{
		WallMs:    wallMs,
		CommandMs: commandMs,
		Note: "wall_ms is what this tool call cost end to end; command_ms is how long the " +
			"command itself ran. The difference is transport and queueing, not the command.",
	}
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

# 🔎 SEARCHING THE CODEBASE
This tool IS the search tool — there is no separate grep/glob tool. Prefer
ripgrep, and keep every search SCOPED YOURSELF; nothing scopes it for you:
- Use 'rg' when available, falling back to 'grep -r' / 'find' when it is not.
- ALWAYS search from a relative path ('rg pattern .', 'rg pattern internal/'),
  never an absolute root.
- Exclude vendored trees, which are large and rarely what you want:
  rg --glob '!node_modules' --glob '!.git' --glob '!dist' --glob '!vendor'
  ('rg' already honours .gitignore, which usually covers these.)
- Bound the results: 'rg -l' for filenames only, 'rg -m 20', or pipe to 'head'.
  An unbounded match dump can exhaust the output budget on a large repo.

# 🚫 NEVER SCAN THE FILESYSTEM
Commands like 'find / -name ...', 'find ~ ...' or 'grep -r ... /usr' are REFUSED
before they run: they read the whole machine, take minutes, and starve every
other agent on this disk.
- To find something in the project → search a relative path, as above.
- To find a DEPENDENCY's source on disk → ask the package manager, do not scan:
  'go list -m -f "{{.Dir}}" <module>', 'go env GOMODCACHE', 'npm root'.
- If you truly need a filesystem search, name a specific directory.

# ⛓️ CHAIN PROBES — ONE CALL ANSWERS MANY QUESTIONS
A turn costs a full model generation whether it carries ONE probe or NINE. The
command string is where you buy that back: separate probes with ';' and label
each with an echo, and one call returns the whole picture.

    ls internal/handlers/; echo "=== proto ==="; grep -n '^message\|^service' \
      proto/services/foo/v1/foo.proto | head -40; echo "=== migrations ==="; \
      ls db/migrations/ | tail -5

Chaining also works when a probe DEPENDS on the previous one, which parallel
tool calls cannot express:

    f=$(rg -l 'ListWidgetsRequest' proto/); echo "$f"; grep -n 'ListWidgets' -A 12 "$f"

Reach for it whenever you are about to run a second search to interpret the
first. Measured: agents that chain average ~8 probes per call; agents that do
not average ~3, and the difference is turns — the single largest recoverable
cost in a long run.

Independent tool calls can ALSO run in parallel in one response. Chaining and
parallel calls compose: issue every call you have already decided in the SAME
turn, and chain the probes inside each one. Never split calls you have already
decided across turns — that is the one pattern that costs a generation for
nothing.

# Output Processing
- Default output limit is 16000 bytes (use max_output to customize)
- Use tail_lines to get only the last N lines of output
- Output metadata includes truncation info and original size

Usage notes:
- The command argument is required.
- You can specify an optional timeout in milliseconds (up to 600000ms / 10 minutes). If not specified, commands will timeout after 60 seconds.
- Use 'run_in_background: true' to run long-running commands in the background. You can then use BashOutput to check output, BashKill to terminate, and BashList to see all running processes.
- Searching the codebase IS a use of this tool (prefer 'rg', scoped to a relative path — see above). For reading whole files, prefer the View tool over 'cat'/'head'/'tail'.
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

	// Refuse filesystem-wide scans before dispatch rather than letting them run
	// to the timeout. See shell_search_guard.go.
	if refusal := unscopedSearchRefusal(params.Command); refusal != "" {
		return NewTextErrorResponse(refusal), nil
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

	workingDir, err := ResolveRepoPath(rctx, params.Repo)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if workingDir == "" {
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

		bgOutput := BashBackgroundOutput{
			ProcessID:    processID,
			Command:      params.Command,
			Backgrounded: true,
		}
		bgJSON, _ := json.Marshal(bgOutput)
		response := WithResponseMetadata(NewTextResponse(string(bgJSON)), metadata)
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
		// Truncate stdout/stderr individually before JSON marshaling
		// to keep the JSON envelope intact
		stdout = truncateOutputWithLimit(stdout, maxOutput)
		stderr = truncateOutputWithLimit(stderr, maxOutput/2)
	}

	if interrupted {
		if stderr != "" {
			stderr += "\n"
		}
		stderr += "Command was aborted before completion"
	}

	// Ensure the JSON-encoded result fits the global output budget. Otherwise the
	// generic tool_wrapper truncation head+tail-cuts the JSON *string*, corrupting
	// the envelope (and appending a misleading "use offset parameter" hint the
	// foreground bash tool doesn't support). Fitting it here keeps the model's
	// tool result valid JSON.
	stdout, stderr = fitBashOutputToBudget(stdout, stderr, exitCode, MaxOutputSize)

	wasTruncated := len(stdout) < originalStdoutSize || len(stderr) < originalStderrSize

	endTime := time.Now()
	metadata := ShellResponseMetadata{
		StartTime:         startTime.UnixMilli(),
		EndTime:           endTime.UnixMilli(),
		CommandDurationMs: result.DurationMs,
		OutputSize:        len(stdout) + len(stderr),
		Truncated:         wasTruncated,
		OriginalSize:      originalStdoutSize + originalStderrSize,
	}

	output := BashOutput{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
		Timing:   bashTransportTiming(endTime.Sub(startTime).Milliseconds(), result.DurationMs),
	}
	outputJSON, _ := json.Marshal(output)

	return WithResponseMetadata(NewTextResponse(string(outputJSON)), metadata), nil
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

// fitBashOutputToBudget shrinks stdout/stderr (head+tail, always re-truncating
// from the passed-in strings so markers never compound) until the JSON-encoded
// BashOutput fits within budget bytes.
//
// The per-stream caps applied earlier bound stdout and stderr individually, but
// their sum plus the JSON envelope and escaping can still exceed MaxOutputSize —
// at which point tool_wrapper's generic truncation cuts through the JSON string
// and corrupts the envelope. Keeping the encoded result under budget means the
// wrapper leaves it untouched and the model always receives valid JSON.
//
// Encoded length grows monotonically with content length, so scaling content
// down by the overflow ratio converges in a few iterations; the bounded loop and
// 1-byte floor guarantee termination.
func fitBashOutputToBudget(stdout, stderr string, exitCode, budget int) (string, string) {
	encodedLen := func(o, e string) int {
		b, _ := json.Marshal(BashOutput{Stdout: o, Stderr: e, ExitCode: exitCode})
		return len(b)
	}
	if encodedLen(stdout, stderr) <= budget {
		return stdout, stderr
	}

	o, e := stdout, stderr
	for i := 0; i < 12; i++ {
		enc := encodedLen(o, e)
		if enc <= budget {
			break
		}
		scale := float64(budget) / float64(enc) * 0.9
		newO := max(int(float64(len(o))*scale), 1)
		newE := max(int(float64(len(e))*scale), 1)
		o = truncateOutputWithLimit(stdout, newO)
		e = truncateOutputWithLimit(stderr, newE)
	}
	return o, e
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
