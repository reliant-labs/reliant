// Copyright (c) 2025 Reliant Labs
package historyrepair

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/models/message"
)

func user(text string) message.Message {
	return message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func assistant(parts ...message.ContentPart) message.Message {
	return message.Message{Role: message.Assistant, Parts: parts}
}

func text(s string) message.TextContent { return message.TextContent{Text: s} }

func call(id, name string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Input: `{"path":"a"}`}
}

func result(id, content string) message.ToolResult {
	return message.ToolResult{ToolCallID: id, Name: "read", Content: content}
}

func tool(results ...message.ToolResult) message.Message {
	parts := make([]message.ContentPart, len(results))
	for i, r := range results {
		parts[i] = r
	}
	return message.Message{Role: message.Tool, Parts: parts}
}

func stub(id, name string) message.ToolResult {
	return message.ToolResult{ToolCallID: id, Name: name, Content: MissingToolResultContent, IsError: true}
}

// shape renders a history as one line per message, so a table row states the
// whole expected history at a glance.
func shape(ms []message.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		var parts []string
		for _, p := range m.Parts {
			switch p := p.(type) {
			case message.TextContent:
				parts = append(parts, "text:"+p.Text)
			case message.ReasoningContent:
				parts = append(parts, "thinking:"+p.Thinking+"/"+p.Signature)
			case message.RedactedReasoningContent:
				parts = append(parts, "redacted:"+p.Data)
			case message.ToolCall:
				parts = append(parts, "call:"+p.ID+":"+p.Input)
			case message.ToolResult:
				c := p.Content
				if c == MissingToolResultContent {
					c = "STUB"
				}
				parts = append(parts, "result:"+p.ToolCallID+":"+c)
			case message.BinaryContent:
				parts = append(parts, "binary:"+p.MIMEType)
			case message.Finish:
				parts = append(parts, "finish")
			}
		}
		out[i] = string(m.Role) + " " + strings.Join(parts, " ")
	}
	return out
}

// healthyHistory is a well-formed conversation: every rule must leave it as
// it is.
func healthyHistory() []message.Message {
	return []message.Message{
		{Role: message.System, Parts: []message.ContentPart{text("be brief")}},
		user("read a and b"),
		assistant(message.ReasoningContent{Thinking: "two reads", Signature: "sig1"}, text("reading"), call("toolu_1", "read"), call("toolu_2", "read")),
		tool(result("toolu_1", "A")),
		tool(result("toolu_2", "B")),
		assistant(text("partial answer"), message.Finish{Reason: message.FinishReasonPauseTurn}),
		assistant(message.RedactedReasoningContent{Data: "sealed"}, text("done")),
		user("thanks"),
		user("one more thing"),
	}
}

func TestApply_HealthyHistoryIsUnchanged(t *testing.T) {
	for name, rules := range map[string]Rules{
		"default":   {},
		"anthropic": {ToolCallIDs: AnthropicIDRules()},
		"openai":    {ToolCallIDs: IDRules{MaxLen: 40}},
	} {
		t.Run(name, func(t *testing.T) {
			in := healthyHistory()
			out, r := Apply(in, rules)
			assert.Equal(t, in, out)
			assert.Equal(t, Report{}, r)
		})
	}
}

func TestApply_Rules(t *testing.T) {
	tests := []struct {
		name   string
		rules  Rules
		in     []message.Message
		want   []string
		report Report
	}{
		{
			name:   "agent message is sent as user",
			in:     []message.Message{user("hi"), assistant(text("hello")), {Role: message.Agent, Parts: []message.ContentPart{text("[mailbox] result")}}},
			want:   []string{"user text:hi", "assistant text:hello", "user text:[mailbox] result"},
			report: Report{AgentMessages: 1},
		},
		{
			name:   "orphaned tool result with no call anywhere is dropped",
			in:     []message.Message{user("hi"), tool(result("gone", "x")), assistant(text("hello"))},
			want:   []string{"user text:hi", "assistant text:hello"},
			report: Report{OrphanedResults: 1, DroppedMessages: 0},
		},
		{
			name:   "orphaned result beside a valid one is dropped alone",
			in:     []message.Message{user("go"), assistant(call("c1", "read")), tool(result("c1", "A"), result("gone", "x"))},
			want:   []string{"user text:go", "assistant call:c1:{\"path\":\"a\"}", "tool result:c1:A"},
			report: Report{OrphanedResults: 1},
		},
		{
			name:   "missing result is stubbed, the call and its thinking kept",
			in:     []message.Message{user("go"), assistant(message.ReasoningContent{Thinking: "t", Signature: "s"}, call("c1", "read"), call("c2", "edit")), tool(result("c1", "A")), user("next")},
			want:   []string{"user text:go", "assistant thinking:t/s call:c1:{\"path\":\"a\"} call:c2:{\"path\":\"a\"}", "tool result:c1:A result:c2:STUB", "user text:next"},
			report: Report{StubbedResults: 1},
		},
		{
			name:   "trailing unanswered call is stubbed",
			in:     []message.Message{user("go"), assistant(call("c1", "read"))},
			want:   []string{"user text:go", "assistant call:c1:{\"path\":\"a\"}", "tool result:c1:STUB"},
			report: Report{StubbedResults: 1},
		},
		{
			name:   "misplaced result is moved next to its call",
			in:     []message.Message{user("go"), assistant(call("c1", "read")), user("interrupt"), tool(result("c1", "A"))},
			want:   []string{"user text:go", "assistant call:c1:{\"path\":\"a\"}", "tool result:c1:A", "user text:interrupt"},
			report: Report{MovedResults: 1},
		},
		{
			name:   "second result for one call is dropped",
			in:     []message.Message{user("go"), assistant(call("c1", "read")), tool(result("c1", "A")), tool(result("c1", "again"))},
			want:   []string{"user text:go", "assistant call:c1:{\"path\":\"a\"}", "tool result:c1:A"},
			report: Report{DuplicateResults: 1},
		},
		{
			name: "repeated call id is renamed with the results that follow it",
			in: []message.Message{
				user("go"), assistant(call("call_0", "read")), tool(result("call_0", "first")),
				assistant(call("call_0", "read")), tool(result("call_0", "second")),
			},
			want: []string{
				"user text:go", "assistant call:call_0:{\"path\":\"a\"}", "tool result:call_0:first",
				"assistant call:call_0_dup1:{\"path\":\"a\"}", "tool result:call_0_dup1:second",
			},
			report: Report{DuplicateCallIDs: 1},
		},
		{
			name:   "repeated id within one turn pairs results in order",
			in:     []message.Message{user("go"), assistant(call("x", "read"), call("x", "read")), tool(result("x", "1"), result("x", "2"))},
			want:   []string{"user text:go", "assistant call:x:{\"path\":\"a\"} call:x_dup1:{\"path\":\"a\"}", "tool result:x:1 result:x_dup1:2"},
			report: Report{DuplicateCallIDs: 1},
		},
		{
			name:   "invalid and non-object tool input becomes {}",
			in:     []message.Message{user("go"), assistant(message.ToolCall{ID: "a", Name: "n", Input: `{"trunc`}, message.ToolCall{ID: "b", Name: "n"}, message.ToolCall{ID: "c", Name: "n", Input: "null"}, message.ToolCall{ID: "d", Name: "n", Input: "[1]"}), tool(result("a", "1"), result("b", "2"), result("c", "3"), result("d", "4"))},
			want:   []string{"user text:go", "assistant call:a:{} call:b:{} call:c:{} call:d:{}", "tool result:a:1 result:b:2 result:c:3 result:d:4"},
			report: Report{InvalidToolInputs: 4},
		},
		{
			name:   "blank text parts are dropped, the rest kept",
			in:     []message.Message{{Role: message.User, Parts: []message.ContentPart{text("  \n"), text("real")}}, assistant(text(" "), text("answer"))},
			want:   []string{"user text:real", "assistant text:answer"},
			report: Report{BlankTextParts: 2},
		},
		{
			name:   "empty user message gets placeholder text",
			in:     []message.Message{user("hi"), assistant(text("hello")), user("   ")},
			want:   []string{"user text:hi", "assistant text:hello", "user text:(empty)"},
			report: Report{BlankTextParts: 1, PlaceholderMessages: 1},
		},
		{
			name:   "empty assistant, tool and system messages are dropped",
			in:     []message.Message{user("hi"), assistant(text(""), message.Finish{}), {Role: message.System}, tool(), assistant(text("hello"))},
			want:   []string{"user text:hi", "assistant text:hello"},
			report: Report{BlankTextParts: 1, DroppedMessages: 3},
		},
		{
			name:   "reasoning-only assistant turn is kept",
			in:     []message.Message{user("hi"), assistant(message.ReasoningContent{Signature: "sig"}), user("again")},
			want:   []string{"user text:hi", "assistant thinking:/sig", "user text:again"},
			report: Report{},
		},
		{
			name:   "history opening with an assistant turn gets a placeholder user turn",
			in:     []message.Message{assistant(text("summary of earlier work")), user("continue")},
			want:   []string{"user text:(empty)", "assistant text:summary of earlier work", "user text:continue"},
			report: Report{OpeningPlaceholder: 1},
		},
		{
			name: "the placeholder goes after leading system messages",
			in: []message.Message{
				{Role: message.System, Parts: []message.ContentPart{text("rules")}},
				assistant(text("summary")), user("continue"),
			},
			want:   []string{"system text:rules", "user text:(empty)", "assistant text:summary", "user text:continue"},
			report: Report{OpeningPlaceholder: 1},
		},
		{
			name:   "an orphan-only opening is dropped and the assistant turn gets a placeholder",
			in:     []message.Message{tool(result("gone", "x")), assistant(text("summary"))},
			want:   []string{"user text:(empty)", "assistant text:summary"},
			report: Report{OrphanedResults: 1, OpeningPlaceholder: 1},
		},
		{
			name:  "consecutive user turns are merged only when the rules say so",
			rules: Rules{MergeConsecutiveUserTurns: true},
			in: []message.Message{
				user("a"), {Role: message.User, Parts: []message.ContentPart{text("b"), message.BinaryContent{MIMEType: "image/png"}}},
				{Role: message.Agent, Parts: []message.ContentPart{text("c")}}, assistant(text("ok")),
			},
			want:   []string{"user text:a text:b binary:image/png text:c", "assistant text:ok"},
			report: Report{AgentMessages: 1, MergedUserTurns: 2},
		},
		{
			name:  "a tool message is never merged into a user turn",
			rules: Rules{MergeConsecutiveUserTurns: true},
			in:    []message.Message{user("go"), assistant(call("c1", "read")), tool(result("c1", "A")), user("x"), user("y")},
			want: []string{
				"user text:go", "assistant call:c1:{\"path\":\"a\"}", "tool result:c1:A", "user text:x text:y",
			},
			report: Report{MergedUserTurns: 1},
		},
		{
			name:  "consecutive assistant turns are never merged",
			rules: Rules{MergeConsecutiveUserTurns: true},
			in:    []message.Message{user("go"), assistant(text("one")), assistant(text("two"))},
			want:  []string{"user text:go", "assistant text:one", "assistant text:two"},
		},
		{
			name:  "ids outside the charset are rewritten on calls and results alike",
			rules: Rules{ToolCallIDs: AnthropicIDRules()},
			in:    []message.Message{user("go"), assistant(call("functions.read:0", "read")), tool(result("functions.read:0", "A"))},
			want: []string{
				"user text:go", "assistant call:" + rewriteID("functions.read:0", AnthropicIDRules()) + ":{\"path\":\"a\"}",
				"tool result:" + rewriteID("functions.read:0", AnthropicIDRules()) + ":A",
			},
			report: Report{RewrittenIDs: 1},
		},
		{
			name:  "overlong ids are rewritten",
			rules: Rules{ToolCallIDs: IDRules{MaxLen: 40}},
			in:    []message.Message{user("go"), assistant(call(strings.Repeat("a", 41), "read")), tool(result(strings.Repeat("a", 41), "A"))},
			want: []string{
				"user text:go", "assistant call:" + rewriteID(strings.Repeat("a", 41), IDRules{MaxLen: 40}) + ":{\"path\":\"a\"}",
				"tool result:" + rewriteID(strings.Repeat("a", 41), IDRules{MaxLen: 40}) + ":A",
			},
			report: Report{RewrittenIDs: 1},
		},
		{
			name:  "zero id rules replay ids verbatim, signature-carrying ones included",
			in:    []message.Message{user("go"), assistant(call("call_ab__thought__c2ln+/=", "read")), tool(result("call_ab__thought__c2ln+/=", "A"))},
			want:  []string{"user text:go", "assistant call:call_ab__thought__c2ln+/=:{\"path\":\"a\"}", "tool result:call_ab__thought__c2ln+/=:A"},
			rules: Rules{},
		},
		{
			name: "a renamed duplicate keeps a carried thought signature as its tail",
			in: []message.Message{
				user("go"), assistant(call("c__thought__sig", "read")), tool(result("c__thought__sig", "1")),
				assistant(call("c__thought__sig", "read")), tool(result("c__thought__sig", "2")),
			},
			want: []string{
				"user text:go", "assistant call:c__thought__sig:{\"path\":\"a\"}", "tool result:c__thought__sig:1",
				"assistant call:c_dup1__thought__sig:{\"path\":\"a\"}", "tool result:c_dup1__thought__sig:2",
			},
			report: Report{DuplicateCallIDs: 1},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, r := Apply(tc.in, tc.rules)
			assert.Equal(t, tc.want, shape(out))
			assert.Equal(t, tc.report, r)

			// Every rule is idempotent: a repaired history is a healthy one.
			again, r2 := Apply(out, tc.rules)
			assert.Equal(t, out, again, "Apply must be idempotent")
			assert.Equal(t, Report{}, r2, "a repaired history needs no further repair")
		})
	}
}

// TestApply_DoesNotModifyTheStoredHistory: the repair works on a copy.
func TestApply_DoesNotModifyTheStoredHistory(t *testing.T) {
	in := []message.Message{
		{Role: message.Agent, Parts: []message.ContentPart{text(" "), text("note")}},
		assistant(message.ToolCall{ID: "functions.x:0", Name: "x", Input: "bad"}),
		user("a"), user("b"),
	}
	before := shape(in)
	inputBefore := in[1].Parts[0].(message.ToolCall).Input
	_, _ = Apply(in, Rules{ToolCallIDs: AnthropicIDRules(), MergeConsecutiveUserTurns: true})
	assert.Equal(t, before, shape(in))
	assert.Equal(t, message.Agent, in[0].Role)
	assert.Equal(t, inputBefore, in[1].Parts[0].(message.ToolCall).Input)
	assert.Len(t, in, 4)
}

// TestApply_IDMappingIsStableAcrossRequests: the same stored id gets the same
// rewritten id on every request, and as the history grows, so the provider's
// prompt cache keeps matching the prefix.
func TestApply_IDMappingIsStableAcrossRequests(t *testing.T) {
	rules := Rules{ToolCallIDs: AnthropicIDRules()}
	turn1 := []message.Message{user("go"), assistant(call("functions.read:0", "read")), tool(result("functions.read:0", "A"))}
	turn2 := append(append([]message.Message(nil), turn1...),
		assistant(text("read it"), call("functions.read:1", "read")), tool(result("functions.read:1", "B")), user("thanks"))

	out1, _ := Apply(turn1, rules)
	out2, _ := Apply(turn2, rules)
	require.Equal(t, out1, out2[:len(out1)], "the repaired prefix must not change as the history grows")

	id := out2[1].ToolCalls()[0].ID
	assert.True(t, idAccepted(id, AnthropicIDRules()))
	assert.Equal(t, id, out2[2].ToolResults()[0].ToolCallID)
	assert.NotEqual(t, id, out2[3].ToolCalls()[0].ID, "distinct ids stay distinct")
	assert.LessOrEqual(t, len(id), 40)
}

// TestApply_ThinkingOrderAndSignaturesArePreserved: no rule reorders or edits
// an assistant turn's reasoning, so Anthropic's signature still verifies.
func TestApply_ThinkingOrderAndSignaturesArePreserved(t *testing.T) {
	turn := assistant(
		message.ReasoningContent{Thinking: "plan", Signature: "sig-A"},
		message.RedactedReasoningContent{Data: "sealed"},
		text("doing it"),
		call("toolu_1", "read"),
	)
	in := []message.Message{user("go"), turn, user("interrupted before the result")}
	out, r := Apply(in, Rules{ToolCallIDs: AnthropicIDRules()})
	assert.Equal(t, 1, r.StubbedResults)
	assert.Equal(t, turn, out[1], "the assistant turn must be byte-identical")
	assert.Equal(t, []message.ContentPart{stub("toolu_1", "read")}, out[2].Parts)
}

// TestForRequest_LogsNothingForAHealthyHistory exercises the logging wrapper.
func TestForRequest_ReturnsTheRepairedHistory(t *testing.T) {
	in := []message.Message{user("go"), assistant(call("c1", "read"))}
	out := ForRequest(t.Context(), in, Rules{})
	assert.Equal(t, []string{"user text:go", "assistant call:c1:{\"path\":\"a\"}", "tool result:c1:STUB"}, shape(out))
	assert.Equal(t, healthyHistory(), ForRequest(t.Context(), healthyHistory(), Rules{}))
}

func TestRewriteID_FitsShortLimits(t *testing.T) {
	id := rewriteID("anything", IDRules{Charset: true, MaxLen: 9})
	assert.Len(t, id, 9)
	assert.True(t, idAccepted(id, IDRules{Charset: true, MaxLen: 9}))
}
