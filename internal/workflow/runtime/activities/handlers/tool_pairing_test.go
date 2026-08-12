// Copyright (c) 2025 Reliant Labs
//
// Unit tests for ValidateToolPairing, the canonical definition of the
// tool-call/tool-result invariant. Everything else in the codebase asserts
// against this function, so its own correctness is worth pinning down directly.
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func asstWithCalls(id string, calls ...message.ToolCall) message.Message {
	parts := make([]message.ContentPart, 0, len(calls))
	for _, c := range calls {
		parts = append(parts, c)
	}
	return message.Message{ID: id, Role: message.Assistant, Parts: parts}
}

func toolWithResults(id string, results ...message.ToolResult) message.Message {
	parts := make([]message.ContentPart, 0, len(results))
	for _, r := range results {
		parts = append(parts, r)
	}
	return message.Message{ID: id, Role: message.Tool, Parts: parts}
}

func TestValidateToolPairing(t *testing.T) {
	tests := []struct {
		name       string
		msgs       []message.Message
		wantKinds  []ToolPairingViolationKind
		wantCallID string
	}{
		{
			name: "valid single pair",
			msgs: []message.Message{
				{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
				asstWithCalls("a1", message.ToolCall{ID: "tc_1", Name: "bash"}),
				toolWithResults("t1", message.ToolResult{ToolCallID: "tc_1", Name: "bash", Content: "out"}),
			},
		},
		{
			name: "valid multi-call batch answered in one tool message",
			msgs: []message.Message{
				asstWithCalls("a1",
					message.ToolCall{ID: "tc_1", Name: "bash"},
					message.ToolCall{ID: "tc_2", Name: "view"}),
				toolWithResults("t1",
					message.ToolResult{ToolCallID: "tc_1", Name: "bash", Content: "a"},
					message.ToolResult{ToolCallID: "tc_2", Name: "view", Content: "b"}),
			},
		},
		{
			name: "no tools at all is valid",
			msgs: []message.Message{
				{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
				{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
			},
		},
		{
			name: "empty history is valid",
			msgs: nil,
		},
		{
			name: "missing result",
			msgs: []message.Message{
				asstWithCalls("a1", message.ToolCall{ID: "tc_1", Name: "bash"}),
			},
			wantKinds:  []ToolPairingViolationKind{ViolationMissingResult},
			wantCallID: "tc_1",
		},
		{
			name: "partially answered batch reports only the unanswered call",
			msgs: []message.Message{
				asstWithCalls("a1",
					message.ToolCall{ID: "tc_1", Name: "bash"},
					message.ToolCall{ID: "tc_2", Name: "view"}),
				toolWithResults("t1", message.ToolResult{ToolCallID: "tc_1", Name: "bash", Content: "a"}),
			},
			wantKinds:  []ToolPairingViolationKind{ViolationMissingResult},
			wantCallID: "tc_2",
		},
		{
			name: "orphaned result with no call",
			msgs: []message.Message{
				toolWithResults("t1", message.ToolResult{ToolCallID: "ghost", Content: "x"}),
			},
			wantKinds:  []ToolPairingViolationKind{ViolationOrphanedResult},
			wantCallID: "ghost",
		},
		{
			name: "result present but not adjacent",
			msgs: []message.Message{
				asstWithCalls("a1", message.ToolCall{ID: "tc_1", Name: "bash"}),
				{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "interrupting"}}},
				toolWithResults("t1", message.ToolResult{ToolCallID: "tc_1", Name: "bash", Content: "late"}),
			},
			wantKinds:  []ToolPairingViolationKind{ViolationResultNotImmediate},
			wantCallID: "tc_1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateToolPairing(tc.msgs)

			if len(tc.wantKinds) == 0 {
				assert.Empty(t, got, "expected a valid history, got: %s", summarizeViolations(got))
				return
			}

			require.Len(t, got, len(tc.wantKinds), "violations: %s", summarizeViolations(got))
			for i, kind := range tc.wantKinds {
				assert.Equal(t, kind, got[i].Kind)
			}
			if tc.wantCallID != "" {
				assert.Equal(t, tc.wantCallID, got[0].ToolCallID)
			}
		})
	}
}

// TestValidateToolPairing_DuplicateResult covers the case the schema now makes
// unrepresentable at rest (tool_call_results.tool_call_id is a PRIMARY KEY), but
// which can still be constructed in memory — so the validator must catch it.
func TestValidateToolPairing_DuplicateResult(t *testing.T) {
	msgs := []message.Message{
		asstWithCalls("a1", message.ToolCall{ID: "tc_1", Name: "bash"}),
		toolWithResults("t1", message.ToolResult{ToolCallID: "tc_1", Name: "bash", Content: "first"}),
		toolWithResults("t2", message.ToolResult{ToolCallID: "tc_1", Name: "bash", Content: "second"}),
	}

	got := ValidateToolPairing(msgs)
	require.NotEmpty(t, got)

	var sawDuplicate bool
	for _, v := range got {
		if v.Kind == ViolationDuplicateResult && v.ToolCallID == "tc_1" {
			sawDuplicate = true
		}
	}
	assert.True(t, sawDuplicate, "duplicate results must be reported, got: %s", summarizeViolations(got))
}

// TestSummarizeViolations_Caps keeps a pathological history from producing an
// unbounded log line or error string.
func TestSummarizeViolations_Caps(t *testing.T) {
	var many []ToolPairingViolation
	for i := 0; i < 50; i++ {
		many = append(many, ToolPairingViolation{Kind: ViolationMissingResult, ToolCallID: "tc"})
	}
	summary := summarizeViolations(many)
	assert.Contains(t, summary, "and 40 more")
	assert.Less(t, len(summary), 2000, "summary must stay bounded for logging")
}
