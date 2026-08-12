// Copyright (c) 2025 Reliant Labs
package message

import (
	"strings"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// TOKEN LIMIT CONSTANTS
// ============================================================================

const (
	// MaxContextTokens is the hard limit for context tokens (200k for Claude)
	MaxContextTokens = 200000

	// SafeContextTokens is the LEGACY, model-unaware fallback threshold used only
	// when a caller cannot supply the model's real context window (contextWindow
	// <= 0). Prefer the model-aware backstop derived from the real window via
	// deriveSafeContextTokens — see TrimBackstopFraction. This fixed value assumes
	// a 200k window and is far too low for large-window models (e.g. 1M), which is
	// why trimming must always be driven by the real window when it is known.
	SafeContextTokens = 195000

	// TrimBackstopFraction is the fraction of a model's REAL context window at
	// which the trim BACKSTOP engages. It sits ABOVE the compaction threshold
	// (~85% of the window) so that compaction — which summarizes older context
	// into a handoff — is the PRIMARY context-management mechanism. Trimming, which
	// head/tail-shreds tool output and degrades the session, is only a last-resort
	// safety net that engages if compaction did not run or did not bring the
	// context back down below this level.
	TrimBackstopFraction = 0.95

	// CharsPerToken is the estimated character-to-token ratio
	// Conservative estimate: 4 characters per token
	CharsPerToken = 4

	// TrimmedContentSuffix is appended to content that was trimmed
	TrimmedContentSuffix = "\n<system>output was trimmed due to exceeding token limits</system>"
)

// deriveSafeContextTokens returns the token count above which the trim backstop
// engages for a model with the given REAL context window (~95% of the window).
// When the window is unknown (<= 0) it falls back to the fixed SafeContextTokens
// so callers without a resolved model keep the legacy behavior.
func deriveSafeContextTokens(contextWindow int64) int {
	if contextWindow <= 0 {
		return SafeContextTokens
	}
	return int(float64(contextWindow) * TrimBackstopFraction)
}

// ============================================================================
// TOOL DEFINITION INTERFACE
// ============================================================================

// ToolDefinition represents a tool for token estimation purposes.
// This interface avoids import cycles with the tools package.
type ToolDefinition interface {
	Name() string
	Description() string
	// ParamSchemaJSON returns the JSON representation of the parameter schema
	// for token estimation. Returns nil if schema is not available.
	ParamSchemaJSON() []byte
}

// ============================================================================
// CONTEXT WINDOW PROTECTION
// ============================================================================

// ContextEstimate holds the breakdown of token estimates for debugging
type ContextEstimate struct {
	MessageTokens      int
	SystemPromptTokens int
	ToolTokens         int
	TotalTokens        int
}

// EstimateFullContextTokens estimates the total token count including messages,
// system prompts, and tool definitions. This provides a complete picture of
// what will be sent to the API.
//
// The algorithm iterates backwards from the end of messages:
//   - Messages without token data are estimated from character count
//   - When a message with TokenCount is found, it represents the total context
//     at that point (including system prompts and tools)
//   - If no message has token data, system prompts and tools are estimated separately
func EstimateFullContextTokens(messages []Message, systemPrompts []string, tools []ToolDefinition) ContextEstimate {
	estimate := ContextEstimate{}
	foundTokenData := false

	// Iterate backwards from the end
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]

		if hasTokenData(&msg) {
			// Found message with token data - use TokenCount directly
			// This includes the full context at that API call (system prompts + tools + all prior messages)
			estimate.MessageTokens += int(msg.TokenCount)
			foundTokenData = true
			break
		}

		// No token data yet - estimate from chars
		estimate.MessageTokens += estimateMessageChars(msg) / CharsPerToken
	}

	// If we never found token data, also estimate system prompts and tools
	// (otherwise they're already included in TokenCount)
	if !foundTokenData {
		// Estimate system prompt tokens
		for _, prompt := range systemPrompts {
			estimate.SystemPromptTokens += len(prompt)
		}
		estimate.SystemPromptTokens /= CharsPerToken

		// Estimate tool definition tokens
		// Each tool has: name, description, and JSON schema
		for _, tool := range tools {
			chars := len(tool.Name()) + len(tool.Description())
			if schemaJSON := tool.ParamSchemaJSON(); schemaJSON != nil {
				chars += len(schemaJSON)
			}
			estimate.ToolTokens += chars
		}
		estimate.ToolTokens /= CharsPerToken
	}

	estimate.TotalTokens = estimate.MessageTokens + estimate.SystemPromptTokens + estimate.ToolTokens
	return estimate
}

// hasTokenData returns true if the message has a stored token count
func hasTokenData(msg *Message) bool {
	return msg.TokenCount > 0
}

// estimateMessageChars calculates the character count for a single message
func estimateMessageChars(msg Message) int {
	count := 0
	for _, part := range msg.Parts {
		count += estimatePartChars(part)
	}
	return count
}

// estimatePartChars calculates the character count for a content part
func estimatePartChars(part ContentPart) int {
	switch p := part.(type) {
	case TextContent:
		return len(p.Text)
	case ReasoningContent:
		return len(p.Thinking)
	case ToolCall:
		return len(p.Name) + len(p.Input)
	case ToolResult:
		return len(p.Content)
	case BinaryContent:
		// Base64 encoded data is ~1.33x the original size
		return len(p.Data) * 4 / 3
	case ImageURLContent:
		return len(p.URL)
	default:
		return 0
	}
}

// TrimMessagesToFitContextWithFullEstimate is the legacy, model-UNAWARE entry
// point. It delegates to TrimMessagesToFitContextWindow with contextWindow=0,
// which uses the fixed SafeContextTokens threshold. Prefer
// TrimMessagesToFitContextWindow with the model's real context window so the
// backstop scales with the model instead of assuming a 200k window.
func TrimMessagesToFitContextWithFullEstimate(messages []Message, systemPrompts []string, tools []ToolDefinition) bool {
	return TrimMessagesToFitContextWindow(messages, systemPrompts, tools, 0)
}

// TrimMessagesToFitContextWindow trims message content to fit within the model's
// context window, accounting for the full token count including system prompts
// and tool definitions.
//
// This is the model-aware BACKSTOP: the trim threshold is derived from the
// model's real context window (~95% of it via TrimBackstopFraction), which sits
// ABOVE the compaction threshold (~85%). Compaction is the primary mechanism;
// this trim only engages when compaction failed to bring the context down. Pass
// contextWindow<=0 when the real window is unknown to fall back to the fixed
// SafeContextTokens threshold.
//
// This function provides accurate context window protection by considering:
// - Message content tokens
// - System prompt tokens
// - Tool definition tokens (name, description, parameter schema)
//
// The function modifies messages in-place and returns whether any trimming occurred.
// It trims from the end of the conversation, prioritizing tool results for trimming.
//
// TOOL-PAIR SAFETY (load-bearing for the tool_use/tool_result invariant):
// Trimming is deliberately TOPOLOGY-PRESERVING. It only ever shortens the string
// payload of a part that is already there; it never removes a part, never removes
// a message, and never rewrites an identifier. That is what keeps it incapable of
// orphaning a tool pair:
//
//   - A ToolCall is never trimmed at all (trimPart has no ToolCall case), so an
//     assistant message's tool_use blocks always survive intact.
//   - A ToolResult is rewritten in place with its ToolCallID carried over, so a
//     TOOL message can never lose the linkage to the call it answers.
//   - No code path here deletes from the messages slice or from Parts, so a TOOL
//     message can never disappear out from under the assistant message before it.
//
// A tool_use block and its tool_result are therefore an ATOMIC UNIT with respect
// to trimming: both survive, or neither is touched. If you ever add a cut that
// drops whole parts or messages, it MUST cut on pair boundaries — dropping a TOOL
// message while its assistant message survives (or vice versa) produces a history
// the provider rejects and the conversation deadlocks on.
// TestTrimming_IsTopologyPreserving enforces this.
func TrimMessagesToFitContextWindow(messages []Message, systemPrompts []string, tools []ToolDefinition, contextWindow int64) bool {
	if len(messages) == 0 {
		return false
	}

	safeLimit := deriveSafeContextTokens(contextWindow)

	estimate := EstimateFullContextTokens(messages, systemPrompts, tools)
	if estimate.TotalTokens <= safeLimit {
		return false
	}

	logging.Warn("[CONTEXT_TRIM] Context exceeds safe limit, trimming messages",
		"totalTokens", estimate.TotalTokens,
		"messageTokens", estimate.MessageTokens,
		"systemPromptTokens", estimate.SystemPromptTokens,
		"toolTokens", estimate.ToolTokens,
		"safeLimit", safeLimit,
		"contextWindow", contextWindow,
		"tokensOver", estimate.TotalTokens-safeLimit)

	// Calculate how many tokens we need to trim from messages
	// We can only trim message content, not system prompts or tool definitions
	tokensToTrim := estimate.TotalTokens - safeLimit
	charsToTrim := tokensToTrim * CharsPerToken

	// Calculate how much the last message can contribute
	lastMsgIdx := len(messages) - 1
	lastMsgChars := estimateMessageChars(messages[lastMsgIdx])

	// Try to trim from the last message first
	trimmed := false
	remainingCharsToTrim := charsToTrim

	if lastMsgChars > 0 {
		// Determine how much the last message can trim (up to 90% of its content)
		maxTrimFromLast := int(float64(lastMsgChars) * 0.9)
		if maxTrimFromLast > 0 {
			trimmed = trimLastMessage(messages, min(charsToTrim, lastMsgChars))
			if trimmed {
				remainingCharsToTrim -= maxTrimFromLast
			}
		}
	}

	// If we still need more trimming, try trimming large tool results from earlier messages
	if remainingCharsToTrim > 0 {
		if trimLargeToolResults(messages, remainingCharsToTrim) {
			return true
		}
	}

	return trimmed
}

// trimLastMessage attempts to trim the last message to reduce character count.
// Returns true if trimming was performed.
func trimLastMessage(messages []Message, charsToTrim int) bool {
	lastMsgIdx := len(messages) - 1
	lastMsg := &messages[lastMsgIdx]

	// Calculate total chars in last message
	lastMsgChars := estimateMessageChars(*lastMsg)
	if lastMsgChars == 0 {
		return false
	}

	// Calculate the fraction of content to keep
	keepRatio := float64(lastMsgChars-charsToTrim) / float64(lastMsgChars)
	if keepRatio < 0.1 {
		keepRatio = 0.1 // Keep at least 10% of content
	}

	logging.Info("[CONTEXT_TRIM] Trimming last message",
		"role", lastMsg.Role,
		"lastMsgChars", lastMsgChars,
		"charsToTrim", charsToTrim,
		"keepRatio", keepRatio)

	// Trim each part in the last message proportionally
	trimmed := false
	for partIdx, part := range lastMsg.Parts {
		trimmedPart := trimPart(part, keepRatio)
		if trimmedPart != nil {
			lastMsg.Parts[partIdx] = trimmedPart
			trimmed = true
		}
	}

	return trimmed
}

// trimLargeToolResults finds and trims large tool results throughout the conversation.
// This is a fallback when the last message alone can't free enough space.
func trimLargeToolResults(messages []Message, charsToTrim int) bool {
	// Find all tool results and their sizes
	type toolResultLocation struct {
		msgIdx  int
		partIdx int
		chars   int
	}
	var toolResults []toolResultLocation

	for msgIdx, msg := range messages {
		for partIdx, part := range msg.Parts {
			if tr, ok := part.(ToolResult); ok {
				chars := len(tr.Content)
				if chars > 10000 { // Only consider large tool results (>10k chars)
					toolResults = append(toolResults, toolResultLocation{
						msgIdx:  msgIdx,
						partIdx: partIdx,
						chars:   chars,
					})
				}
			}
		}
	}

	if len(toolResults) == 0 {
		return false
	}

	// Sort by size (largest first) and trim the largest ones
	// Simple bubble sort since we expect few large results
	for i := 0; i < len(toolResults)-1; i++ {
		for j := i + 1; j < len(toolResults); j++ {
			if toolResults[j].chars > toolResults[i].chars {
				toolResults[i], toolResults[j] = toolResults[j], toolResults[i]
			}
		}
	}

	trimmed := false
	remainingToTrim := charsToTrim

	for _, loc := range toolResults {
		if remainingToTrim <= 0 {
			break
		}

		tr := messages[loc.msgIdx].Parts[loc.partIdx].(ToolResult)

		// Calculate how much to keep (at least 20% to maintain context)
		keepRatio := 0.2
		if float64(loc.chars)*0.8 > float64(remainingToTrim) {
			// We don't need to trim this much
			keepRatio = float64(loc.chars-remainingToTrim) / float64(loc.chars)
		}

		newLen := int(float64(len(tr.Content)) * keepRatio)
		if newLen < len(tr.Content) {
			logging.Info("[CONTEXT_TRIM] Trimming large tool result",
				"messageIdx", loc.msgIdx,
				"toolName", tr.Name,
				"originalChars", loc.chars,
				"keepRatio", keepRatio)

			// Copy the struct and overwrite only Content. Listing fields
			// explicitly here silently dropped BinaryParts (images/PDFs
			// attached to a tool result) every time a large result was
			// trimmed, because the literal simply omitted the field.
			trimmedResult := tr
			trimmedResult.Content = trimWithHeadTail(tr.Content, newLen) + TrimmedContentSuffix
			messages[loc.msgIdx].Parts[loc.partIdx] = trimmedResult

			remainingToTrim -= (loc.chars - newLen)
			trimmed = true
		}
	}

	return trimmed
}

// trimPart trims a content part to the given ratio, preserving head and tail.
// Returns nil when the part must not be shortened.
//
// ToolCall is deliberately absent from the switch: trimming a tool call's input
// would corrupt the arguments the model asked for, and truncating its ID would
// orphan the matching tool_result. Tool calls pass through untouched.
func trimPart(part ContentPart, keepRatio float64) ContentPart {
	switch p := part.(type) {
	case TextContent:
		newLen := int(float64(len(p.Text)) * keepRatio)
		if newLen < len(p.Text) {
			return TextContent{Text: trimWithHeadTail(p.Text, newLen) + TrimmedContentSuffix}
		}
	case ToolResult:
		newLen := int(float64(len(p.Content)) * keepRatio)
		if newLen < len(p.Content) {
			// Copy-and-overwrite rather than re-listing fields: the field list
			// omitted BinaryParts, so trimming a tool result silently discarded
			// its attached images/PDFs. ToolCallID must survive verbatim or the
			// result no longer answers its tool_use block.
			trimmed := p
			trimmed.Content = trimWithHeadTail(p.Content, newLen) + TrimmedContentSuffix
			return trimmed
		}
	case ReasoningContent:
		newLen := int(float64(len(p.Thinking)) * keepRatio)
		if newLen < len(p.Thinking) {
			return ReasoningContent{Thinking: trimWithHeadTail(p.Thinking, newLen) + TrimmedContentSuffix}
		}
		// ToolCall, BinaryContent, ImageURLContent - don't trim these
	}
	return nil // No trimming needed
}

// trimWithHeadTail trims content to targetLen, keeping head and tail portions.
// This preserves context from the beginning and end of the output.
func trimWithHeadTail(content string, targetLen int) string {
	if len(content) <= targetLen {
		return content
	}

	if targetLen <= 0 {
		return "[content trimmed]"
	}

	// Reserve space for the ellipsis marker
	ellipsis := "\n\n... [content trimmed] ...\n\n"
	availableLen := targetLen - len(ellipsis)
	if availableLen <= 0 {
		return "[content trimmed]"
	}

	// Split 60/40 between head and tail (head usually has more context)
	headLen := availableLen * 60 / 100
	tailLen := availableLen - headLen

	// Try to break at line boundaries for cleaner output
	head := content[:headLen]
	if lastNewline := strings.LastIndex(head, "\n"); lastNewline > headLen/2 {
		head = content[:lastNewline]
	}

	tailStart := len(content) - tailLen
	tail := content[tailStart:]
	if firstNewline := strings.Index(tail, "\n"); firstNewline > 0 && firstNewline < tailLen/2 {
		tail = content[tailStart+firstNewline+1:]
	}

	return head + ellipsis + tail
}
