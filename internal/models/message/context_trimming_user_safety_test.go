// Copyright (c) 2025 Reliant Labs
//
// The trim backstop vs. the user's own words.
//
// Prod incident 2026-10-09: GPT models on the codex driver kept answering "your
// message was truncated — I only received the literal `[content trimmed]`".
// The user's message was intact in the DB; the trim backstop rewrote it on the
// way to the provider. Its first move was always "trim the LAST message", which
// on a user turn IS the user's request, and a short request trimmed to 10% of
// itself is shorter than the ellipsis marker, so it became "[content trimmed]".
//
// The backstop exists to shred bulky tool output. These tests pin that it never
// touches anything a person wrote.
package message

import (
	"strings"
	"testing"
)

const terraWindow = 272_000 // gpt-5.6-terra's max_context_window (models.yaml)

// TestTrimming_InflatedTokenCount_NewestUserMessageSurvives reproduces the
// incident exactly: chat 5ffd6bb4's assistant turns carried token_count 520k /
// 696k against a 272k window, so every turn was "over" the backstop, and the
// newest user message was the first thing the trimmer shredded.
func TestTrimming_InflatedTokenCount_NewestUserMessageSurvives(t *testing.T) {
	const userRequest = "ok so whats the status on the clickhouse integration tests now?"
	msgs := []Message{
		{Role: User, Parts: []ContentPart{TextContent{Text: "run the clickhouse tests"}}},
		{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "tc_1", Name: "shell", Input: `{"command":"go test ./..."}`}}},
		{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "tc_1", Name: "shell", Content: strings.Repeat("ok  pkg\n", 2_000)}}},
		// The stored token_count from the prod row (seq 1315, gpt-5.6-terra).
		{Role: Assistant, Parts: []ContentPart{TextContent{Text: "Tests are running."}}, TokenCount: 696_512},
		{Role: User, Parts: []ContentPart{TextContent{Text: userRequest}}},
	}

	TrimMessagesToFitContextWindow(msgs, nil, nil, terraWindow)

	got := msgs[len(msgs)-1].Parts[0].(TextContent).Text
	if got != userRequest {
		t.Fatalf("newest user message was rewritten by the trim backstop:\n got: %q\nwant: %q", got, userRequest)
	}
}

// TestTrimming_NeverTrimsUserMessages holds even when the context is REALLY over
// the backstop (a believable token count, below the window): the trimmer must
// find its room in tool output, never in what the user wrote.
func TestTrimming_NeverTrimsUserMessages(t *testing.T) {
	bigToolOutput := strings.Repeat("x", 400_000)
	pastedLog := strings.Repeat("paste ", 30_000) // a long user paste: 180k chars

	tests := []struct {
		name string
		msgs func() []Message
	}{
		{
			name: "short newest user turn after an over-limit assistant turn",
			msgs: func() []Message {
				return []Message{
					{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "tc_1", Name: "view", Input: "{}"}}},
					{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "tc_1", Name: "view", Content: bigToolOutput}}},
					{Role: Assistant, Parts: []ContentPart{TextContent{Text: "done"}}, TokenCount: 265_000},
					{Role: User, Parts: []ContentPart{TextContent{Text: "now fix it"}}},
				}
			},
		},
		{
			name: "long pasted user turn is not shredded",
			msgs: func() []Message {
				return []Message{
					{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "tc_1", Name: "view", Input: "{}"}}},
					{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "tc_1", Name: "view", Content: bigToolOutput}}},
					{Role: Assistant, Parts: []ContentPart{TextContent{Text: "done"}}, TokenCount: 250_000},
					{Role: User, Parts: []ContentPart{TextContent{Text: pastedLog}}},
				}
			},
		},
		{
			name: "several queued user turns after the last assistant message",
			msgs: func() []Message {
				return []Message{
					{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "tc_1", Name: "view", Input: "{}"}}},
					{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "tc_1", Name: "view", Content: bigToolOutput}}},
					{Role: Assistant, Parts: []ContentPart{TextContent{Text: "done"}}, TokenCount: 265_000},
					{Role: User, Parts: []ContentPart{TextContent{Text: "first, read the error"}}},
					{Role: User, Parts: []ContentPart{TextContent{Text: "second, " + pastedLog}}},
				}
			},
		},
		{
			name: "earlier user message with no token data anywhere (char estimate path)",
			msgs: func() []Message {
				return []Message{
					{Role: User, Parts: []ContentPart{TextContent{Text: pastedLog}}},
					{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "tc_1", Name: "view", Input: "{}"}}},
					{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "tc_1", Name: "view", Content: strings.Repeat("y", 1_200_000)}}},
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := tc.msgs()
			want := userTexts(msgs)

			if !TrimMessagesToFitContextWindow(msgs, nil, nil, terraWindow) {
				t.Fatal("precondition: this context is over the backstop and has tool output to trim")
			}

			got := userTexts(msgs)
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("user message %d was trimmed (%d -> %d chars, now ends %q)",
						i, len(want[i]), len(got[i]), tail(got[i], 80))
				}
			}
		})
	}
}

// TestTrimming_OnlyUserContentLeft_LeavesItAlone: when nothing but user-authored
// text is over the limit there is nothing the backstop may cut. Sending the
// user's words intact (and letting the provider/compaction deal with size) is
// always better than sending the model "[content trimmed]" instead of them.
func TestTrimming_OnlyUserContentLeft_LeavesItAlone(t *testing.T) {
	text := strings.Repeat("u", 1_200_000)
	msgs := []Message{{Role: User, Parts: []ContentPart{TextContent{Text: text}}}}

	if TrimMessagesToFitContextWindow(msgs, nil, nil, terraWindow) {
		t.Error("reported a trim with no tool output to trim")
	}
	if got := msgs[0].Parts[0].(TextContent).Text; got != text {
		t.Errorf("user text was modified: %d -> %d chars", len(text), len(got))
	}
}

// TestEstimate_TokenCountAboveWindowIsUnreliable: a stored TokenCount larger
// than the model's context window cannot describe a context the model holds.
// It must not drive trimming; the estimate falls back to counting characters
// (including the system prompt and tool definitions it would otherwise assume
// were inside the count).
func TestEstimate_TokenCountAboveWindowIsUnreliable(t *testing.T) {
	msgs := []Message{
		{Role: User, Parts: []ContentPart{TextContent{Text: strings.Repeat("u", 4_000)}}},                         // 1000 tokens
		{Role: Assistant, Parts: []ContentPart{TextContent{Text: strings.Repeat("a", 400)}}, TokenCount: 696_512}, // 100 tokens
		{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "tc_1", Content: strings.Repeat("t", 40_000)}}},  // 10000 tokens
	}
	systemPrompts := []string{strings.Repeat("s", 8_000)}                                         // 2000 tokens
	tools := []ToolDefinition{mockToolDef{name: "tool", description: strings.Repeat("d", 3_996)}} // 1000 tokens

	est := estimateContextTokens(msgs, systemPrompts, tools, terraWindow)
	if est.TotalTokens != 1_000+100+10_000+2_000+1_000 {
		t.Errorf("estimate = %+v, want the char-based total 14100 (the 696k count is above the 272k window)", est)
	}

	// A count inside the window is still trusted, exactly as before.
	msgs[1].TokenCount = 120_000
	est = estimateContextTokens(msgs, systemPrompts, tools, terraWindow)
	if est.TotalTokens != 120_000+10_000 {
		t.Errorf("estimate = %+v, want 130000 (in-window count + chars after it)", est)
	}

	// With the window unknown there is nothing to compare against: trusted.
	msgs[1].TokenCount = 696_512
	if est := EstimateFullContextTokens(msgs, systemPrompts, tools); est.TotalTokens != 696_512+10_000 {
		t.Errorf("window-unaware estimate = %+v, want 706512", est)
	}
}

// TestTrimming_InflatedTokenCountDoesNotDriveTrimming: the same impossible count
// on a context that is actually small must not trim anything at all — before
// the fix it shredded every large tool result in the history every turn.
func TestTrimming_InflatedTokenCountDoesNotDriveTrimming(t *testing.T) {
	toolOutput := strings.Repeat("r", 60_000) // 15k tokens: a normal, keepable result
	msgs := []Message{
		{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "tc_1", Name: "view", Input: "{}"}}},
		{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "tc_1", Name: "view", Content: toolOutput}}},
		{Role: Assistant, Parts: []ContentPart{TextContent{Text: "looked"}}, TokenCount: 520_244},
		{Role: User, Parts: []ContentPart{TextContent{Text: "and?"}}},
	}

	if TrimMessagesToFitContextWindow(msgs, nil, nil, terraWindow) {
		t.Error("trimmed a ~15k-token context because of an impossible 520k token_count")
	}
	if got := msgs[1].Parts[0].(ToolResult).Content; got != toolOutput {
		t.Errorf("tool result was trimmed: %d -> %d chars", len(toolOutput), len(got))
	}
}

func userTexts(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != User {
			continue
		}
		for _, p := range m.Parts {
			if tc, ok := p.(TextContent); ok {
				out = append(out, tc.Text)
			}
		}
	}
	return out
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
