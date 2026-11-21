// Copyright (c) 2025 Reliant Labs

// Package historyrepairtest holds what every driver's history-repair test
// shares: one damaged history, and a validator per wire format that checks a
// converted request is one the provider would accept.
//
// Each driver test repairs BrokenHistory with the driver's own rules
// (historyrepair.Apply(BrokenHistory(), c.HistoryRules())), converts it with
// the driver's converter, and hands the request, marshalled to JSON, to the
// validator for its wire format.
package historyrepairtest

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// IDs of BrokenHistory's tool calls.
const (
	// ForeignCallID is an id Anthropic refuses (a Kimi-style id relayed by
	// another provider before a model switch).
	ForeignCallID = "functions.read:0"
	// AnsweredByNothing is the id of a result no call asked for.
	AnsweredByNothing = "toolu_gone"

	// LongCallID is an id longer than OpenAI's 64-character Responses
	// limit, in a charset Anthropic accepts.
	LongCallID = "call_kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"
)

// BrokenHistory is a history with one of each defect the shared repair fixes:
// it opens with an assistant turn (a compaction summary), has two user turns
// in a row, a tool call with unparseable input and a foreign id whose result
// arrives after an interrupting user message, an orphaned result beside it,
// a blank text part, and ends on an unanswered tool call.
func BrokenHistory() []message.Message {
	return []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "summary of earlier work"}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "read the file"}}},
		{Role: message.Agent, Parts: []message.ContentPart{message.TextContent{Text: "[inbox] a teammate replied"}, message.TextContent{Text: " "}}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "reading"},
			message.ToolCall{ID: ForeignCallID, Name: "read", Input: `{"path":"a`},
		}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "are you done?"}}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: ForeignCallID, Name: "read", Content: "contents of a"},
			message.ToolResult{ToolCallID: AnsweredByNothing, Name: "read", Content: "stale"},
		}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: LongCallID, Name: "read", Input: `{"path":"b"}`},
		}},
	}
}

func decode(t *testing.T, wire any) []map[string]any {
	t.Helper()
	b, err := json.Marshal(wire)
	require.NoError(t, err)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

func blocks(m map[string]any, key string) []map[string]any {
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		if bm, ok := b.(map[string]any); ok {
			out = append(out, bm)
		}
	}
	return out
}

var anthropicID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// CheckAnthropicMessages validates an Anthropic Messages API messages array:
// it opens with a user turn, every tool_use is answered by a tool_result in
// the very next message, every tool_result answers a tool_use in the message
// before it, and every id matches ^[a-zA-Z0-9_-]+$.
func CheckAnthropicMessages(t *testing.T, wire any) {
	t.Helper()
	msgs := decode(t, wire)
	require.NotEmpty(t, msgs)
	require.Equal(t, "user", msgs[0]["role"], "the request must open with a user turn")
	var pending map[string]bool
	for i, m := range msgs {
		var results []string
		uses := map[string]bool{}
		for _, b := range blocks(m, "content") {
			switch b["type"] {
			case "tool_use":
				id, _ := b["id"].(string)
				require.Regexp(t, anthropicID, id)
				uses[id] = true
			case "tool_result":
				id, _ := b["tool_use_id"].(string)
				require.Regexp(t, anthropicID, id)
				results = append(results, id)
			}
		}
		for _, id := range results {
			require.True(t, pending[id], "message %d: tool_result %q answers no tool_use in the message before it", i, id)
			delete(pending, id)
		}
		require.Empty(t, pending, "message %d: tool_use ids left unanswered", i)
		pending = uses
	}
	require.Empty(t, pending, "the request ends with unanswered tool_use ids")
}

// CheckChatMessages validates an OpenAI Chat Completions messages array:
// every assistant tool call is answered by the tool messages directly after
// it, every tool message answers one of them, and no id is longer than
// maxIDLen (0: no limit) or, when charset is set, outside [a-zA-Z0-9_-].
func CheckChatMessages(t *testing.T, wire any, maxIDLen int, charset bool) {
	t.Helper()
	msgs := decode(t, wire)
	checkID := func(id string) {
		require.NotEmpty(t, id)
		if maxIDLen > 0 {
			require.LessOrEqual(t, len(id), maxIDLen, "tool call id %q", id)
		}
		if charset {
			require.Regexp(t, anthropicID, id)
		}
	}
	pending := map[string]bool{}
	for i, m := range msgs {
		if m["role"] == "tool" {
			id, _ := m["tool_call_id"].(string)
			checkID(id)
			require.True(t, pending[id], "message %d: tool message %q answers no call of the assistant turn before it", i, id)
			delete(pending, id)
			continue
		}
		require.Empty(t, pending, "message %d: tool calls left unanswered", i)
		for _, tc := range blocks(m, "tool_calls") {
			id, _ := tc["id"].(string)
			checkID(id)
			pending[id] = true
		}
	}
	require.Empty(t, pending, "the request ends with unanswered tool calls")
}

// CheckResponsesInput validates an OpenAI Responses API input array: every
// function_call_output answers an earlier function_call, every function_call
// is answered exactly once, and no call_id is longer than maxIDLen.
func CheckResponsesInput(t *testing.T, wire any, maxIDLen int) {
	t.Helper()
	items := decode(t, wire)
	calls := map[string]bool{}
	answered := map[string]bool{}
	for i, it := range items {
		id, _ := it["call_id"].(string)
		switch it["type"] {
		case "function_call":
			require.LessOrEqual(t, len(id), maxIDLen, "call_id %q", id)
			require.False(t, calls[id], "item %d: duplicate function_call %q", i, id)
			calls[id] = true
		case "function_call_output":
			require.True(t, calls[id], "item %d: function_call_output %q answers no earlier function_call", i, id)
			require.False(t, answered[id], "item %d: duplicate function_call_output %q", i, id)
			answered[id] = true
		}
	}
	require.Equal(t, calls, answered, "every function_call needs exactly one output")
}

// CheckGeminiContents validates a Gemini contents array: it opens with a user
// turn, every model turn's function calls are answered by function responses
// in the user turns directly after it, and no two user turns of plain text
// sit side by side.
func CheckGeminiContents(t *testing.T, wire any) {
	t.Helper()
	contents := decode(t, wire)
	require.NotEmpty(t, contents)
	require.Equal(t, "user", contents[0]["role"], "the request must open with a user turn")
	var pending []string
	prevPlainUser := false
	for i, c := range contents {
		var calls, responses []string
		plain := true
		for _, p := range blocks(c, "parts") {
			if fc, ok := p["functionCall"].(map[string]any); ok {
				name, _ := fc["name"].(string)
				calls = append(calls, name)
				plain = false
			}
			if fr, ok := p["functionResponse"].(map[string]any); ok {
				name, _ := fr["name"].(string)
				responses = append(responses, name)
				plain = false
			}
		}
		for _, name := range responses {
			idx := indexOf(pending, name)
			require.GreaterOrEqual(t, idx, 0, "content %d: functionResponse %q answers no call of the model turn before it", i, name)
			pending = append(pending[:idx], pending[idx+1:]...)
		}
		if len(responses) == 0 {
			require.Empty(t, pending, "content %d: function calls left unanswered", i)
		}
		if len(calls) > 0 {
			pending = calls
		}
		isPlainUser := c["role"] == "user" && plain
		require.False(t, isPlainUser && prevPlainUser, "content %d: two plain user turns in a row", i)
		prevPlainUser = isPlainUser
	}
	require.Empty(t, pending, "the request ends with unanswered function calls")
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
