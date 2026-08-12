// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrepareHistoryForLLM_TrimsToolResultsOnToolLessRequest pins the
// trim-before-flatten ordering.
//
// A tool-less request (compaction summarization) flattens every tool block to
// text. When that flatten ran BEFORE the trim, the trim's only bulk-reduction
// mechanism — trimLargeToolResults, which cuts ToolResult parts over 10k chars —
// had nothing left to find: the ToolResults were already TextContent. It freed
// nothing, still reported success, and the caller shipped an oversized request
// that the provider rejected. Trimming first restores the backstop.
// The oversized tool result must sit in the MIDDLE of the history, not last.
// trimLastMessage shortens the final message whatever its shape, so a history
// whose bulk is in the last turn gets trimmed under either ordering and proves
// nothing. Real compaction histories look like this one: huge tool output early
// on, a short user turn at the end. Only trimLargeToolResults can reclaim that,
// and only if it still sees ToolResult parts.
func TestPrepareHistoryForLLM_TrimsToolResultsOnToolLessRequest(t *testing.T) {
	// A single tool result far larger than any plausible context window.
	hugeResult := strings.Repeat("x", 4_000_000) // ~1M tokens at 4 chars/token

	history := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "run it"},
		}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "t1", Name: "bash", Input: `{"cmd":"ls"}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "t1", Name: "bash", Content: hugeResult},
		}},
		// Short trailing turn — the compaction trigger message.
		{Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "summarize the conversation above"},
		}},
	}

	// No tools => tool-less request, the compaction summarization shape.
	got := prepareHistoryForLLM("chat-1", history, []string{"summarize"}, nil, 200_000)

	require.NotEmpty(t, got)

	// The flatten still ran: no tool blocks survive a tool-less request.
	for _, m := range got {
		assert.Empty(t, m.ToolCalls(), "no tool_use blocks may survive a tool-less request")
		assert.Empty(t, m.ToolResults(), "no tool_result blocks may survive a tool-less request")
	}

	// The trim actually freed volume. Before the reorder this assertion failed:
	// the full 4MB payload flowed through untouched.
	var totalChars int
	for _, m := range got {
		for _, p := range m.Parts {
			if tc, ok := p.(message.TextContent); ok {
				totalChars += len(tc.Text)
			}
		}
	}
	assert.Less(t, totalChars, len(hugeResult)/2,
		"trim must shrink the oversized tool result before it is flattened to text")
}
