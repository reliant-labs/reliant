// Copyright (c) 2025 Reliant Labs

package messageproto

import (
	"encoding/json"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// A viewer's transcript keeps every input and leaves every tool result out,
// whole, previews included, except the results the client builds state from
// (plans, tasks, questions).
func TestElideForViewer(t *testing.T) {
	call := reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL
	result := reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT
	text := reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT
	msgs := []*reliantv1.Message{
		{ContentBlocks: []*reliantv1.ContentBlock{
			{Id: "t", Type: text, Content: ptr.Of("I will read the file.")},
			{Id: "c1", Type: call, ToolName: ptr.Of("view"), ToolCallId: ptr.Of("1"), Input: ptr.Of(`{"file_path":".env"}`)},
			{Id: "c2", Type: call, ToolName: ptr.Of("create_plan"), ToolCallId: ptr.Of("2"), Input: ptr.Of(`{"title":"p"}`)},
		}},
		{ContentBlocks: []*reliantv1.ContentBlock{
			{Id: "r1", Type: result, ToolCallId: ptr.Of("1"), Content: ptr.Of("SECRET=hunter2"), Truncated: ptr.Of(true), OriginalSize: ptr.Of(int64(9000))},
			{Id: "r2", Type: result, ToolCallId: ptr.Of("2"), Content: ptr.Of(`{"plan_id":"p"}`)},
		}},
	}
	ElideForViewer(msgs)
	blocks := map[string]*reliantv1.ContentBlock{}
	for _, m := range msgs {
		for _, b := range m.ContentBlocks {
			blocks[b.GetId()] = b
		}
	}
	if r := blocks["r1"]; r.Content != nil || !r.GetBodyElided() || r.GetBodySize() != 9000 || r.Truncated != nil {
		t.Fatalf("a file read's result: %+v", r)
	}
	if r := blocks["r2"]; r.GetContent() != `{"plan_id":"p"}` || r.GetBodyElided() {
		t.Fatalf("a plan result is the conversation's and stays: %+v", r)
	}
	if blocks["c1"].GetInput() != `{"file_path":".env"}` || blocks["t"].GetContent() == "" {
		t.Fatal("inputs and text stay whole")
	}
}

// The JSON form a replayed or live message update carries: the same rule,
// and a payload that cannot be read is never sent as it is.
func TestViewerMessageUpdate(t *testing.T) {
	raw := []byte(`{"message":{"id":"m","content_blocks":[` +
		`{"type":2,"tool_call_id":"1","tool_name":"bash","input":"{\"command\":\"cat .env\"}"},` +
		`{"type":3,"tool_call_id":"1","content":"SECRET=hunter2","truncated":true,"original_size":9000},` +
		`{"type":2,"tool_call_id":"2","tool_name":"ask_user","input":"{}"},` +
		`{"type":3,"tool_call_id":"2","content":"{\"answer\":\"yes\"}"}]}}`)
	out := ViewerMessageUpdate(raw)
	var payload struct {
		Message struct {
			Blocks []map[string]any `json:"content_blocks"`
		} `json:"message"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	b := payload.Message.Blocks
	if _, has := b[1]["content"]; has || b[1]["body_elided"] != true || b[1]["body_size"].(float64) != 9000 {
		t.Fatalf("the shell result: %v", b[1])
	}
	if b[0]["input"] != `{"command":"cat .env"}` {
		t.Fatalf("the call's input: %v", b[0])
	}
	if b[3]["content"] != `{"answer":"yes"}` {
		t.Fatalf("a question's answer stays: %v", b[3])
	}
	if got := string(ViewerMessageUpdate([]byte(`{"content_blocks": [not json`))); got != `{}` {
		t.Fatalf("an unreadable payload: %q, want {}", got)
	}
	if got := string(ViewerMessageUpdate([]byte(`{"title":"x"}`))); got != `{"title":"x"}` {
		t.Fatalf("a payload without blocks is untouched: %q", got)
	}
}

// collidingNames are tool names a third party can produce that end in, or
// spell differently, a built-in whose result a viewer keeps.
func collidingNames(builtin string) []string {
	return []string{
		"mcp__server__" + builtin,
		"mcp__" + builtin + "__" + builtin,
		"mcp__a__b__" + builtin,
		strings.ToUpper(builtin),
		" " + builtin,
	}
}

// A viewer keeps a result whole only for a built-in named exactly: an MCP
// tool whose name ends in one (mcp__server__get_plan), or any other
// spelling, is a third party's output and is elided, on the protobuf path
// (ElideForViewer) and the JSON path (ViewerMessageUpdate) alike.
func TestViewerKeepsResultsOnlyOfExactBuiltIns(t *testing.T) {
	call := reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL
	result := reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT
	for builtin := range viewerKeptResultTools {
		cases := map[string]bool{builtin: true}
		for _, n := range collidingNames(builtin) {
			cases[n] = false
		}
		for name, kept := range cases {
			// Protobuf: the name on the result, on the call only, and on both.
			for _, shape := range []struct{ onCall, onResult string }{{name, ""}, {"", name}, {name, name}} {
				msgs := []*reliantv1.Message{{ContentBlocks: []*reliantv1.ContentBlock{
					{Id: "c", Type: call, ToolCallId: ptr.Of("1"), ToolName: ptr.Of(shape.onCall)},
					{Id: "r", Type: result, ToolCallId: ptr.Of("1"), ToolName: ptr.Of(shape.onResult), Content: ptr.Of("SECRET")},
				}}}
				if shape.onCall == "" {
					msgs[0].ContentBlocks = msgs[0].ContentBlocks[1:]
				}
				ElideForViewer(msgs)
				r := msgs[0].ContentBlocks[len(msgs[0].ContentBlocks)-1]
				if got := r.Content != nil; got != kept {
					t.Errorf("protobuf %q (call %q, result %q): kept %v, want %v", name, shape.onCall, shape.onResult, got, kept)
				}
			}
			// JSON: the name on the result block, and on the call alone.
			for _, raw := range []string{
				`{"content_blocks":[{"type":3,"tool_call_id":"1","tool_name":` + jsonString(name) + `,"content":"SECRET"}]}`,
				`{"message":{"content_blocks":[{"type":2,"tool_call_id":"1","tool_name":` + jsonString(name) + `},{"type":3,"tool_call_id":"1","content":"SECRET"}]}}`,
			} {
				got := strings.Contains(string(ViewerMessageUpdate([]byte(raw))), "SECRET")
				if got != kept {
					t.Errorf("json %q: kept %v, want %v (%s)", name, got, kept, raw)
				}
			}
		}
	}
}

// A result whose two names disagree (a built-in on one, a third party's on
// the other) is elided, and so is one that names no tool at all.
func TestViewerElidesResultsOfMixedOrUnnamedTools(t *testing.T) {
	call := reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL
	result := reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT
	for _, tc := range []struct{ onCall, onResult string }{
		{"mcp__server__get_plan", "get_plan"},
		{"get_plan", "mcp__server__get_plan"},
		{"bash", "get_plan"},
	} {
		msgs := []*reliantv1.Message{{ContentBlocks: []*reliantv1.ContentBlock{
			{Type: call, ToolCallId: ptr.Of("1"), ToolName: ptr.Of(tc.onCall)},
			{Type: result, ToolCallId: ptr.Of("1"), ToolName: ptr.Of(tc.onResult), Content: ptr.Of("SECRET")},
		}}}
		ElideForViewer(msgs)
		if msgs[0].ContentBlocks[1].Content != nil {
			t.Errorf("call %q, result %q: kept", tc.onCall, tc.onResult)
		}
	}
	unnamed := []*reliantv1.Message{{ContentBlocks: []*reliantv1.ContentBlock{
		{Type: result, ToolCallId: ptr.Of("1"), Content: ptr.Of("SECRET")},
	}}}
	ElideForViewer(unnamed)
	if unnamed[0].ContentBlocks[0].Content != nil {
		t.Error("a result naming no tool was kept")
	}
}

// Every tool a viewer keeps the result of is a structured-result built-in:
// the disclosure list can only narrow what the client parses, never widen it.
func TestViewerKeptResultToolsAreBuiltIn(t *testing.T) {
	for name := range viewerKeptResultTools {
		if !structuredResultTools[name] || strings.HasPrefix(name, "mcp__") {
			t.Errorf("%q is not a structured-result built-in", name)
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Every chat update type has a viewer rule, so a type added later reaches a
// viewer only once someone has decided what of it may.
func TestEveryChatUpdateTypeIsClassifiedForViewers(t *testing.T) {
	for v, name := range reliantv1.ChatUpdateType_name {
		if _, ok := viewerChatUpdates[reliantv1.ChatUpdateType(v)]; !ok {
			t.Errorf("%s has no viewer rule in viewerChatUpdates: decide whether its payload may reach a viewer", name)
		}
	}
	if got := ViewerChatUpdate(reliantv1.ChatUpdateType(999), `{"stdout":"SECRET"}`); got != `{}` {
		t.Fatalf("an unclassified type: %q, want {}", got)
	}
}

// A redacted type's payload that is not a JSON object is sent as {}: null
// (which decodes to a nil map) and malformed bodies alike, never as it is
// and never a panic.
func TestViewerChatUpdateNonObjectPayloads(t *testing.T) {
	for _, typ := range []reliantv1.ChatUpdateType{
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_RUN_OUTPUT,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_EXECUTION_LOG,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_EXECUTION,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION,
	} {
		for _, data := range []string{`null`, `{"stdout": "SECRET`, `["SECRET"]`, `"SECRET"`, ``} {
			if got := ViewerChatUpdate(typ, data); got != `{}` {
				t.Errorf("%v %q: %q, want {}", typ, data, got)
			}
		}
	}
	if got := ViewerChatUpdate(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, `null`); got != `null` {
		t.Errorf("a message update with no blocks is sent as it is: %q", got)
	}
}

// An error's raw text is the failure as it happened, which embeds what a
// command or tool printed: a viewer is told that it failed, where and when.
func TestViewerChatUpdateError(t *testing.T) {
	got := ViewerChatUpdate(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR,
		`{"id":"e","activity_type":"run_command","error_message":"exit 1: SECRET=hunter2","error_summary":"SECRET=hunter2","thread":"t","timestamp":"x"}`)
	var payload map[string]any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatal(err)
	}
	if _, has := payload["error_message"]; has {
		t.Fatalf("error_message reached a viewer: %v", payload)
	}
	if _, has := payload["error_summary"]; has {
		t.Fatalf("error_summary reached a viewer: %v", payload)
	}
	if payload["thread"] != "t" || payload["activity_type"] != "run_command" || payload["body_elided"] != true {
		t.Fatalf("where and when it failed stay: %v", payload)
	}
}

// A workflow step's question is a resolved prompt and goes without its
// metadata; an agent's ask_user is the model's own words and stays whole.
func TestViewerChatUpdateQuestion(t *testing.T) {
	step := ViewerChatUpdate(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION,
		`{"question_id":"q","status":"pending","metadata":"{\"question\":\"Deploy with SECRET=hunter2?\"}"}`)
	if strings.Contains(step, "SECRET") || !strings.Contains(step, `"question_id":"q"`) {
		t.Fatalf("a workflow question: %s", step)
	}
	asked := `{"question_id":"q","tool_call_id":"c","metadata":"{\"question\":\"Which branch?\"}"}`
	var got, want map[string]any
	if err := json.Unmarshal([]byte(ViewerChatUpdate(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION, asked)), &got); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(asked), &want)
	if got["metadata"] != want["metadata"] {
		t.Fatalf("an ask_user question: %v", got)
	}
}
