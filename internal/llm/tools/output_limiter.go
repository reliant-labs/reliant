// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/tokens"
)

const (
	// MaxOutputSize is the maximum size of tool output in bytes.
	// 24KB ≈ 6K tokens — prevents a single tool call from consuming too much
	// context. Code is denser than prose (~3 chars/token vs 4), so the byte
	// figure is the conservative one to reason about.
	MaxOutputSize = 24_000

	// TruncationWarningThreshold is when we start warning about large outputs:
	// three quarters of the ceiling, so the warning arrives with room to act
	// rather than at the cliff edge.
	TruncationWarningThreshold = 18_000

	// PaginationChunkSize is the size of chunks for paginated responses
	PaginationChunkSize = 8_000

	// MaxSkillBodySize is the delivered-size ceiling for one skill load. It
	// binds BOTH skill-delivery paths — the skill tool's own action=load
	// result (capped by the ToolWrapper) and the body call_llm seeds into the
	// prompt as a preloaded skill — so a preloaded skill and a hand-loaded
	// skill are byte-identical no matter how large the source file is.
	//
	// It equals MaxOutputSize. A skill is authored content with a known
	// publishing budget — forge renders its shipped SKILL.md files against
	// this same number — so a separate, larger ceiling here would only move
	// the cliff while making every skill silently more expensive on every
	// turn of every run. The fix for an oversize skill is to split it at the
	// source, which is what the truncation notice tells the reader to do.
	MaxSkillBodySize = MaxOutputSize
)

// CapSkillContent enforces MaxSkillBodySize on the text delivered for a skill
// load and reports whether anything was dropped.
//
// Truncation is never silent. When content is dropped the returned text ends
// with an explicit, unmissable notice stating how many bytes went missing and
// telling the reader to open the skill file on disk for the tail. That tail is
// exactly where the skill tool appends its sub-skill, related-skill and
// suggested-tools pointers, so a quiet cut does not merely shorten a skill —
// it severs the links to the rest of the skill tree.
//
// Callers are expected to log when this fires: an oversize skill is a
// publishing defect to fix at the source, not a condition to absorb silently.
func CapSkillContent(content string) (string, bool) {
	if len(content) <= MaxSkillBodySize {
		return content, false
	}

	// The notice's own length depends on the numbers it reports, which depend
	// on how much head survives. Shrink the head until head+notice fits.
	keep := MaxSkillBodySize
	for i := 0; i < 8; i++ {
		notice := skillTruncationNotice(len(content), len(content)-keep)
		if keep+len(notice) <= MaxSkillBodySize {
			break
		}
		keep = MaxSkillBodySize - len(notice)
	}
	if keep < 0 {
		keep = 0
	}

	// Cut on a line boundary so the surviving text ends as readable markdown
	// rather than mid-token — but never give up more than half the budget.
	head := content[:keep]
	if idx := strings.LastIndexByte(head, '\n'); idx > keep/2 {
		head = head[:idx+1]
	}

	notice := skillTruncationNotice(len(content), len(content)-len(head))
	for len(head) > 0 && len(head)+len(notice) > MaxSkillBodySize {
		head = head[:len(head)-1]
		notice = skillTruncationNotice(len(content), len(content)-len(head))
	}
	return head + notice, true
}

// skillTruncationNotice renders the marker appended to a truncated skill.
//
// The recovery instruction names the skill tool's own parameters. It used to
// say "read the skill's SKILL.md directly from disk", which is not an action
// the reader can take: skills reach the agent through the config pipeline —
// embedded in a binary, or synced from a daemon that may not share the
// filesystem — so the named path frequently does not exist on the machine
// running the tool. An unactionable instruction is what pushed agents to shell
// out to `sed -n` and page by hand.
func skillTruncationNotice(total, dropped int) string {
	return fmt.Sprintf(`

=== SKILL TRUNCATED: %d OF %d BYTES WERE DROPPED ===
The text above is only the first %d bytes of this skill. It does NOT end where
it appears to end. Everything after that point was not delivered — including,
if this skill has them, its sub-skill list, related-skill pointers and
suggested-tools list.
To read the rest, call the skill tool again with a window:
  action="load", offset=%d          continue from where this text stops
  action="load", section="<heading>"  fetch one section by name
  action="load", regex="<pattern>"    find the relevant lines first
Treat any instruction that seems to be cut off as incomplete until you have
fetched the remainder. This skill exceeds the %d-byte delivery budget and
should also be split at its source into smaller skills.
=== END SKILL TRUNCATION NOTICE ===
`, dropped, total, total-dropped, total-dropped, MaxSkillBodySize)
}

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

	// Skills are the one tool output where a cut tail changes meaning rather
	// than just costing detail, so they get their own loud notice and skip the
	// generic head/tail machinery entirely. Returning early also guarantees the
	// bytes here are identical to the ones call_llm seeds for a preloaded
	// skill, which calls CapSkillContent directly.
	if toolName == ToolSkill {
		capped, _ := CapSkillContent(output)
		return capped
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
			"Search the file with 'rg pattern <file>' to locate content before reading",
			"Consider reading only the relevant portion of the file",
		}
	case "bash", "powershell", "bash_output":
		return []string{
			"Use head, tail, or a narrower pattern to filter command output",
			"When searching, bound the results: 'rg -l' for filenames only, or 'rg -m 20'",
			"Scope the search to a subdirectory rather than the whole worktree",
			"Redirect verbose output to /dev/null if not needed",
			"Use summary flags (e.g., --summary, --brief) when available",
			"Consider breaking the command into smaller, focused operations",
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
