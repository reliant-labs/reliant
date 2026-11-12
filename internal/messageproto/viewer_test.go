// Copyright (c) 2025 Reliant Labs

package messageproto

import (
	"encoding/json"
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
