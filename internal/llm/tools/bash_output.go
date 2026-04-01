// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type BashOutputParams struct {
	ProcessID string `json:"process_id" jsonschema:"required,description=The ID of the background process to get output from"`

	// Standard pagination
	Offset int `json:"offset,omitempty" jsonschema:"description=Start reading from byte N (default: 0). Can be combined with: limit regex. Cannot be combined with: tail"`
	Limit  int `json:"limit,omitempty" jsonschema:"description=Read up to N bytes (default: 16000). Can be combined with: offset regex. Cannot be combined with: tail"`

	// Tail mode (mutually exclusive with regex)
	Tail int `json:"tail,omitempty" jsonschema:"description=Get last N lines instead of reading from offset. Cannot be combined with: regex regex_case_insensitive regex_context_before regex_context_after offset limit"`

	// Regex filtering
	Regex                string `json:"regex,omitempty" jsonschema:"description=Filter output to lines matching this regex pattern. When set the tool filters first then applies offset/limit to filtered results. Can be combined with: offset limit regex_case_insensitive regex_context_before regex_context_after. Cannot be combined with: tail"`
	RegexCaseInsensitive bool   `json:"regex_case_insensitive,omitempty" jsonschema:"description=Perform case-insensitive regex matching (default: false). Requires: regex. Cannot be combined with: tail"`
	RegexContextBefore   int    `json:"regex_context_before,omitempty" jsonschema:"description=Include N lines before each match like grep -B (default: 0). Requires: regex. Cannot be combined with: tail"`
	RegexContextAfter    int    `json:"regex_context_after,omitempty" jsonschema:"description=Include N lines after each match like grep -C (default: 0). Requires: regex. Cannot be combined with: tail"`
}

type BashOutputResponseMetadata struct {
	ProcessID      string `json:"process_id"`
	Status         string `json:"status"`
	Command        string `json:"command"`
	HasExited      bool   `json:"has_exited"`
	ExitCode       *int   `json:"exit_code,omitempty"`
	BytesRead      int    `json:"bytes_read"`
	TotalAvailable int    `json:"total_available"`
	HasMore        bool   `json:"has_more"`
	NextOffset     int    `json:"next_offset,omitempty"`

	// Filter metadata (when regex is used)
	FilterApplied     bool   `json:"filter_applied,omitempty"`
	FilterPattern     string `json:"filter_pattern,omitempty"`
	TotalMatches      int    `json:"total_matches,omitempty"`
	MatchesInResponse int    `json:"matches_in_response,omitempty"`
	OriginalTotalSize int    `json:"original_total_size,omitempty"`
}

type bashOutputTool struct{}

const (
	BashOutputToolName    = "bash_output"
	bashOutputDescription = `Retrieves output from a background process with pagination and regex filtering support.

WORKSPACE SCOPING:
- Can read output from any process in the current workspace, regardless of which chat started it
- Multiple chats in the same workspace share process visibility
- This enables monitoring: check on servers or builds started by other chats

This tool allows you to check the stdout and stderr output of a process running in the background,
with support for reading in chunks to handle large outputs efficiently and filtering with regex.

Usage notes:
- Process IDs are provided when you start a background process with run_in_background: true
- The tool will indicate if the process is still running or has completed
- If the process has completed, the exit code will be provided
- Output is not cleared after reading - you can re-read from any position

MODES OF OPERATION:

1. Standard Pagination (default):
   - offset: Start reading from byte N (default: 0)
   - limit: Read up to N bytes (default: 16000)
   - Can be combined: offset + limit

2. Tail Mode:
   - tail: Get last N lines
   - Cannot be combined with: regex, offset, limit

3. Regex Filter Mode:
   - regex: Filter output to lines matching pattern
   - When set, tool filters FIRST, then applies offset/limit to filtered results
   - Can be combined with: offset, limit, regex_case_insensitive, regex_context_before, regex_context_after
   - Cannot be combined with: tail
   - Optional parameters:
     * regex_case_insensitive: Case-insensitive matching
     * regex_context_before: Include N lines before match (like grep -B)
     * regex_context_after: Include N lines after match (like grep -A)

PARAMETER COMPATIBILITY:
✓ Valid combinations:
  - offset + limit (standard pagination)
  - tail (alone)
  - regex (alone)
  - regex + offset + limit (filtered pagination)
  - regex + regex_case_insensitive + regex_context_before + regex_context_after

✗ Invalid combinations (will error):
  - tail + regex
  - tail + offset
  - tail + limit
  - regex_case_insensitive without regex
  - regex_context_before/after without regex

Examples:
1. Start a background process:
   bash(command="npm run dev", run_in_background=true)

2. Get first chunk:
   bash_output(process_id="<id>")

3. Get next chunk:
   bash_output(process_id="<id>", offset=16000)

4. Get last 100 lines:
   bash_output(process_id="<id>", tail=100)

5. Filter for errors:
   bash_output(process_id="<id>", regex="ERROR|FATAL")

6. Filter with context:
   bash_output(process_id="<id>", regex="ERROR", regex_context_after=3)

7. Filter and paginate:
   bash_output(process_id="<id>", regex="WARN", offset=0, limit=10000)

The response includes metadata:
- has_more: true if more output is available
- next_offset: where to start reading for the next chunk
- total_available: total bytes available in the (filtered or original) output
- filter_applied: true if regex was used
- total_matches: number of matching lines (when filtered)
- matches_in_response: number of matches in this chunk`
)

func NewBashOutputTool() Tool {
	tool := &bashOutputTool{}
	return NewToolWrapper[BashOutputParams, ToolResponse](tool)
}

func (b *bashOutputTool) Name() string {
	return BashOutputToolName
}

func (b *bashOutputTool) Description() string {
	return bashOutputDescription
}

func (b *bashOutputTool) RequiresPermission(params BashOutputParams) (bool, error) {
	// bash_output tool doesn't require permissions as it's read-only
	return false, nil
}

func (b *bashOutputTool) Execute(rctx *rctx.ToolContext, params BashOutputParams) (ToolResponse, error) {
	if rctx.Daemon == nil {
		return NewTextErrorResponse("command execution requires a connected daemon"), nil
	}

	if params.ProcessID == "" {
		return NewTextErrorResponse("process_id is required"), nil
	}

	// Validate parameter combinations
	if err := validateParams(params); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	// Set default limit
	if params.Limit <= 0 {
		params.Limit = 16000
	}

	// For regex mode with context lines, we need the full output to do context filtering server-side.
	// For simple modes, delegate directly to daemon.
	if params.Regex != "" {
		return b.executeRegexMode(rctx, params)
	}

	// Build daemon output opts for non-regex modes
	opts := &daemon.OutputOpts{
		Offset:    params.Offset,
		Limit:     params.Limit,
		TailLines: params.Tail,
	}

	procOutput, err := rctx.Daemon.GetProcessOutput(rctx.Context, params.ProcessID, opts)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get output: %v", err)), nil
	}

	output := procOutput.Output
	if output == "" && procOutput.TotalBytes == 0 {
		output = "No output yet"
	}

	// Get process info for status display
	processes, _ := rctx.Daemon.ListProcesses(rctx.Context)
	statusInfo := buildDaemonStatusInfo(params.ProcessID, processes)
	output += statusInfo

	metadata := BashOutputResponseMetadata{
		ProcessID:      params.ProcessID,
		BytesRead:      len(procOutput.Output),
		TotalAvailable: procOutput.TotalBytes,
		HasMore:        procOutput.HasMore,
		NextOffset:     procOutput.NextOffset,
	}

	// Fill in process status from list
	fillProcessStatus(&metadata, params.ProcessID, processes)

	return WithResponseMetadata(NewTextResponse(output), metadata), nil
}

// executeRegexMode handles regex filtering with context lines server-side.
// We fetch the full output from the daemon, then apply regex + context + pagination locally.
func (b *bashOutputTool) executeRegexMode(rctx *rctx.ToolContext, params BashOutputParams) (ToolResponse, error) {
	// Get all output (no offset/limit/tail from daemon)
	opts := &daemon.OutputOpts{
		Offset: 0,
		Limit:  0, // 0 = no limit, get everything
	}

	procOutput, err := rctx.Daemon.GetProcessOutput(rctx.Context, params.ProcessID, opts)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get output: %v", err)), nil
	}

	fullOutput := procOutput.Output
	originalTotalSize := len(fullOutput)

	// Get process info for status display
	processes, _ := rctx.Daemon.ListProcesses(rctx.Context)

	// Compile regex
	var re *regexp.Regexp
	if params.RegexCaseInsensitive {
		re, err = regexp.Compile("(?i)" + params.Regex)
	} else {
		re, err = regexp.Compile(params.Regex)
	}

	if err != nil {
		errorOutput := fmt.Sprintf("Invalid regex pattern: %v", err)
		metadata := BashOutputResponseMetadata{
			ProcessID:         params.ProcessID,
			FilterApplied:     false,
			OriginalTotalSize: originalTotalSize,
		}
		fillProcessStatus(&metadata, params.ProcessID, processes)
		return WithResponseMetadata(NewTextResponse(errorOutput), metadata), nil
	}

	// Split output into lines
	lines := splitIntoLines(fullOutput)

	// Find matching lines
	type matchInfo struct {
		lineNum int
		isMatch bool
	}

	matchedLines := make(map[int]matchInfo)
	totalMatches := 0

	for i, line := range lines {
		if re.MatchString(line) {
			matchedLines[i] = matchInfo{lineNum: i + 1, isMatch: true}
			totalMatches++

			// Add context before
			for j := 1; j <= params.RegexContextBefore; j++ {
				contextIdx := i - j
				if contextIdx >= 0 {
					if _, exists := matchedLines[contextIdx]; !exists {
						matchedLines[contextIdx] = matchInfo{lineNum: contextIdx + 1, isMatch: false}
					}
				}
			}

			// Add context after
			for j := 1; j <= params.RegexContextAfter; j++ {
				contextIdx := i + j
				if contextIdx < len(lines) {
					if _, exists := matchedLines[contextIdx]; !exists {
						matchedLines[contextIdx] = matchInfo{lineNum: contextIdx + 1, isMatch: false}
					}
				}
			}
		}
	}

	// Build filtered output with line numbers
	var filteredLines []string
	if len(matchedLines) > 0 {
		// Get sorted line indices
		indices := make([]int, 0, len(matchedLines))
		for idx := range matchedLines {
			indices = append(indices, idx)
		}
		// Simple bubble sort
		for i := 0; i < len(indices); i++ {
			for j := i + 1; j < len(indices); j++ {
				if indices[i] > indices[j] {
					indices[i], indices[j] = indices[j], indices[i]
				}
			}
		}

		for _, idx := range indices {
			info := matchedLines[idx]
			prefix := " "
			if info.isMatch {
				prefix = ">"
			}
			line := strings.TrimSuffix(lines[idx], "\n")
			filteredLines = append(filteredLines, fmt.Sprintf("%s Line %d: %s", prefix, info.lineNum, line))
		}
	}

	filteredOutput := strings.Join(filteredLines, "\n")
	if filteredOutput != "" {
		filteredOutput += "\n"
	}

	// Add filter summary
	filterSummary := fmt.Sprintf("\n=== FILTER INFO ===\nPattern: %s\nCase Insensitive: %v\nTotal Matches: %d\nContext Before: %d\nContext After: %d\nOriginal Output Size: %d bytes\n",
		params.Regex, params.RegexCaseInsensitive, totalMatches, params.RegexContextBefore, params.RegexContextAfter, originalTotalSize)

	filteredOutput += filterSummary

	// Now apply offset/limit to filtered output
	filteredTotalSize := len(filteredOutput)
	var output string
	bytesRead := 0
	nextOffset := 0
	matchesInResponse := totalMatches

	if params.Offset >= filteredTotalSize {
		if filteredTotalSize == 0 {
			output = "No matches found\n" + filterSummary
			matchesInResponse = 0
		} else {
			output = "No more filtered output available (offset exceeds total size)"
		}
	} else {
		endPos := params.Offset + params.Limit
		if endPos > filteredTotalSize {
			endPos = filteredTotalSize
		}
		output = filteredOutput[params.Offset:endPos]
		bytesRead = endPos - params.Offset
		if endPos < filteredTotalSize {
			nextOffset = endPos
		}

		// Count matches in this chunk
		if params.Offset > 0 || endPos < filteredTotalSize {
			matchesInResponse = strings.Count(output, ">")
		}
	}

	// Add status info
	statusInfo := buildDaemonStatusInfo(params.ProcessID, processes)
	output += statusInfo

	metadata := BashOutputResponseMetadata{
		ProcessID:         params.ProcessID,
		BytesRead:         bytesRead,
		TotalAvailable:    filteredTotalSize,
		HasMore:           nextOffset > 0,
		NextOffset:        nextOffset,
		FilterApplied:     true,
		FilterPattern:     params.Regex,
		TotalMatches:      totalMatches,
		MatchesInResponse: matchesInResponse,
		OriginalTotalSize: originalTotalSize,
	}

	fillProcessStatus(&metadata, params.ProcessID, processes)

	return WithResponseMetadata(NewTextResponse(output), metadata), nil
}

func splitIntoLines(s string) []string {
	if s == "" {
		return []string{}
	}
	// Preserve line endings by manually splitting
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// validateParams checks that parameter combinations are valid
func validateParams(params BashOutputParams) error {
	// Check tail + regex conflict
	if params.Tail > 0 && params.Regex != "" {
		return fmt.Errorf("cannot use both 'tail' and 'regex' parameters together")
	}

	// Check tail + offset conflict
	if params.Tail > 0 && params.Offset > 0 {
		return fmt.Errorf("cannot use both 'tail' and 'offset' parameters together")
	}

	// Check tail + limit conflict
	if params.Tail > 0 && params.Limit > 0 && params.Limit != 50000 { // 50000 is default, so ignore it
		return fmt.Errorf("cannot use both 'tail' and 'limit' parameters together")
	}

	// Check regex context without regex
	if params.Regex == "" {
		if params.RegexCaseInsensitive {
			return fmt.Errorf("'regex_case_insensitive' requires 'regex' parameter")
		}
		if params.RegexContextBefore > 0 {
			return fmt.Errorf("'regex_context_before' requires 'regex' parameter")
		}
		if params.RegexContextAfter > 0 {
			return fmt.Errorf("'regex_context_after' requires 'regex' parameter")
		}
	}

	return nil
}

// buildDaemonStatusInfo creates the status section from daemon process info
func buildDaemonStatusInfo(processID string, processes []*daemon.ProcessInfo) string {
	for _, p := range processes {
		if p.ID == processID {
			statusInfo := fmt.Sprintf("\n\n=== STATUS ===\nProcess ID: %s\nStatus: %s\nCommand: %s",
				p.ID, p.Status, p.Command)
			if p.Status != "running" && p.ExitCode != nil {
				statusInfo += fmt.Sprintf("\nExit Code: %d", *p.ExitCode)
			}
			return statusInfo
		}
	}
	return fmt.Sprintf("\n\n=== STATUS ===\nProcess ID: %s", processID)
}

// fillProcessStatus fills metadata fields from daemon process list
func fillProcessStatus(metadata *BashOutputResponseMetadata, processID string, processes []*daemon.ProcessInfo) {
	for _, p := range processes {
		if p.ID == processID {
			metadata.Status = p.Status
			metadata.Command = p.Command
			metadata.HasExited = p.Status != "running"
			metadata.ExitCode = p.ExitCode
			return
		}
	}
}
