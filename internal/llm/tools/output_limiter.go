// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/tokens"
)

const (
	// MaxOutputSize is the maximum size of tool output in bytes
	// 16KB ≈ 4K tokens - prevents single tool calls from consuming too much context
	// Code is denser than prose (~3 chars/token vs 4), so we use a conservative limit
	MaxOutputSize = 16_000

	// TruncationWarningThreshold is when we start warning about large outputs
	TruncationWarningThreshold = 12_000

	// PaginationChunkSize is the size of chunks for paginated responses
	PaginationChunkSize = 8_000
)

// OutputLimitError is returned when output exceeds the maximum allowed size
type OutputLimitError struct {
	ToolName    string
	OutputSize  int
	MaxSize     int
	Suggestions []string
}

func (e *OutputLimitError) Error() string {
	var msg strings.Builder
	fmt.Fprintf(&msg, "Output from %s tool exceeds maximum size limit\n", e.ToolName)
	fmt.Fprintf(&msg, "Output size: %d bytes (%.1f KB)\n", e.OutputSize, float64(e.OutputSize)/1024)
	fmt.Fprintf(&msg, "Maximum allowed: %d bytes (%.1f KB)\n", e.MaxSize, float64(e.MaxSize)/1024)

	if len(e.Suggestions) > 0 {
		msg.WriteString("\nSuggestions to reduce output:\n")
		for _, suggestion := range e.Suggestions {
			fmt.Fprintf(&msg, "  • %s\n", suggestion)
		}
	}

	return msg.String()
}

// CheckOutputSize checks if the output size is within acceptable limits
// Returns the output, a truncation indicator, and any error
func CheckOutputSize(toolName string, output string) (string, bool, error) {
	outputSize := len(output)

	if outputSize <= MaxOutputSize {
		return output, false, nil
	}

	// For certain tools, provide specific suggestions
	suggestions := getToolSpecificSuggestions(toolName)

	return "", true, &OutputLimitError{
		ToolName:    toolName,
		OutputSize:  outputSize,
		MaxSize:     MaxOutputSize,
		Suggestions: suggestions,
	}
}

// TruncateOutput truncates output to fit within size limits
// It tries to truncate intelligently based on the tool type
func TruncateOutput(toolName string, output string, addWarning bool) string {
	if len(output) <= MaxOutputSize {
		return output
	}

	// Calculate how much to keep
	keepSize := MaxOutputSize - 500 // Leave room for warning message

	var truncated string
	switch toolName {
	case "bash", "powershell", "bash_output", "view":
		// For shell and view output, show beginning and end (head+tail strategy)
		if keepSize > 2000 {
			headSize := keepSize * 3 / 4
			tailSize := keepSize / 4
			omittedBytes := len(output) - headSize - tailSize
			truncated = output[:headSize] +
				fmt.Sprintf("\n\n... [%d bytes omitted - use offset parameter to read middle section] ...\n\n", omittedBytes) +
				output[len(output)-tailSize:]
		} else {
			truncated = output[:keepSize]
		}

	case "grep", "glob", "ls":
		// For file listings, truncate at the end
		lines := strings.Split(output, "\n")
		var result strings.Builder
		currentSize := 0
		linesIncluded := 0

		for _, line := range lines {
			lineSize := len(line) + 1 // +1 for newline
			if currentSize+lineSize > keepSize {
				break
			}
			if linesIncluded > 0 {
				result.WriteString("\n")
			}
			result.WriteString(line)
			currentSize += lineSize
			linesIncluded++
		}

		truncated = result.String()
		if linesIncluded < len(lines) {
			truncated += fmt.Sprintf("\n\n... [TRUNCATED - Showing %d of %d results] ...",
				linesIncluded, len(lines))
		}

	case "fetch":
		// For web content, try to keep the beginning
		truncated = output[:keepSize] + "\n\n... [CONTENT TRUNCATED] ..."

	default:
		// Generic truncation
		truncated = output[:keepSize] + "\n\n... [OUTPUT TRUNCATED] ..."
	}

	if addWarning {
		originalTokens := tokens.EstimateTokens(output)
		truncatedTokens := tokens.EstimateTokens(truncated)
		warning := fmt.Sprintf("\n\n⚠️ Output truncated from %d bytes (~%dK tokens) to %d bytes (~%dK tokens).",
			len(output), (originalTokens+500)/1000, len(truncated), (truncatedTokens+500)/1000)
		// Add tool-specific guidance
		switch toolName {
		case "view":
			warning += " Use offset parameter to read remaining content."
		case "grep":
			warning += " Use more specific pattern or path, or use files_with_matches mode."
		case "glob", "ls":
			warning += " Use more specific pattern or path."
		default:
			warning += " Consider using more specific parameters."
		}
		truncated = warning + "\n\n" + truncated
	}

	return truncated
}

// getToolSpecificSuggestions returns suggestions for reducing output based on tool type
func getToolSpecificSuggestions(toolName string) []string {
	switch toolName {
	case "view":
		return []string{
			"Use offset and limit parameters to read specific sections",
			"Use grep to find specific content before reading",
			"Consider reading only the relevant portion of the file",
		}
	case "bash", "powershell", "bash_output":
		return []string{
			"Use head, tail, or grep to filter command output",
			"Redirect verbose output to /dev/null if not needed",
			"Use summary flags (e.g., --summary, --brief) when available",
			"Consider breaking the command into smaller, focused operations",
		}
	case "grep":
		return []string{
			"Use more specific search patterns",
			"Limit search to specific file types with --type flag",
			"Use --files-with-matches mode instead of content mode",
			"Specify a smaller search path",
			"Use head_limit parameter to limit results",
		}
	case "glob", "ls":
		return []string{
			"Use more specific patterns to match fewer files",
			"Limit search depth with appropriate glob patterns",
			"Search in a more specific directory",
		}
	case "fetch":
		return []string{
			"The web page content is too large",
			"Consider fetching a specific section if possible",
			"Try a different format (text vs markdown)",
		}
	default:
		return []string{
			"Try to be more specific in your request",
			"Consider breaking the operation into smaller parts",
			"Use filtering or limiting parameters if available",
		}
	}
}

// ShouldWarnAboutSize returns true if the output size is large enough to warrant a warning
func ShouldWarnAboutSize(outputSize int) bool {
	return outputSize >= TruncationWarningThreshold
}
