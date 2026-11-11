// Copyright (c) 2026 Reliant Labs

package messageproto

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
)

const (
	stub        = reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB
	full        = reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL
	unspecified = reliantv1.ToolBodyMode_TOOL_BODY_MODE_UNSPECIFIED
)

func bigEditInput(t *testing.T, n int) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"file_path":  "internal/x.go",
		"old_string": strings.Repeat("o", n),
		"new_string": strings.Repeat("n", n),
	})
	require.NoError(t, err)
	return string(b)
}

func callBlock(tool, input, callID string) *db.MessageContentBlock {
	return &db.MessageContentBlock{
		ID: "blk-" + callID, BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName: ptr.Of(tool), ToolCallID: ptr.Of(callID), ToolInput: ptr.Of(input),
	}
}

func callRow(id string, status core.ToolCallStatus, thread string) *db.ToolCall {
	now := time.Now()
	row := &db.ToolCall{ID: id, ToolName: "x", Status: status, ThreadID: ptr.Of(thread)}
	if status.IsTerminal() {
		row.CompletedAt = &now
	}
	return row
}

func TestResolveToolBodyMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		requested reliantv1.ToolBodyMode
		setting   string
		want      reliantv1.ToolBodyMode
	}{
		{unspecified, "on", full},
		{full, "on", full},
		{stub, "on", stub},
		{stub, "", stub},    // no config (a test, or a nil block): stubs on
		{stub, "off", full}, // the kill switch
		{unspecified, "off", full},
		{reliantv1.ToolBodyMode(99), "on", full}, // a mode this server does not know
	} {
		assert.Equal(t, tc.want, ResolveToolBodyMode(tc.requested, tc.setting), "%v setting=%q", tc.requested, tc.setting)
	}
}

// TestStubToolCallPolicy is the TOOL_CALL row of the design's §2 table.
func TestStubToolCallPolicy(t *testing.T) {
	t.Parallel()
	large := bigEditInput(t, 2000)

	for _, tc := range []struct {
		name     string
		tool     string
		input    string
		status   *core.ToolCallStatus // nil: no durable row
		thread   string               // the call's thread; the view is "main"
		wantStub bool
	}{
		{name: "finished, large", tool: "edit", input: large, status: ptr.Of(core.ToolCallStatusCompleted), thread: "main", wantStub: true},
		{name: "failed, large", tool: "edit", input: large, status: ptr.Of(core.ToolCallStatusFailed), thread: "main", wantStub: true},
		{name: "cancelled, large", tool: "edit", input: large, status: ptr.Of(core.ToolCallStatusCancelled), thread: "main", wantStub: true},
		{name: "executing is in flight", tool: "edit", input: large, status: ptr.Of(core.ToolCallStatusExecuting), thread: "main"},
		{name: "pending (an approval renders the input)", tool: "edit", input: large, status: ptr.Of(core.ToolCallStatusPending), thread: "main"},
		{name: "backgrounded is still running", tool: "shell", input: large, status: ptr.Of(core.ToolCallStatusBackgrounded), thread: "main"},
		{name: "no durable row", tool: "edit", input: large},
		{name: "inherited in-flight call is shown cancelled, so it is finished here", tool: "edit", input: large, status: ptr.Of(core.ToolCallStatusExecuting), thread: "source", wantStub: true},
		{name: "always-full: structured tool", tool: "create_plan", input: large, status: ptr.Of(core.ToolCallStatusCompleted), thread: "main"},
		{name: "always-full: request_machine", tool: "request_machine", input: large, status: ptr.Of(core.ToolCallStatusCompleted), thread: "main"},
		{name: "always-full: MCP prefix", tool: "mcp__reliant__ask_user", input: large, status: ptr.Of(core.ToolCallStatusCompleted), thread: "main"},
		{name: "always-full: prefixed draft_automation", tool: "reliant__draft_automation", input: large, status: ptr.Of(core.ToolCallStatusCompleted), thread: "main"},
		{name: "always-full: generate_image", tool: "generate_image", input: large, status: ptr.Of(core.ToolCallStatusCompleted), thread: "main"},
		{name: "always-full: get_run", tool: "get_run", input: large, status: ptr.Of(core.ToolCallStatusCompleted), thread: "main"},
		{name: "exactly at the threshold", tool: "edit", input: padJSON(t, StubBodyThreshold), status: ptr.Of(core.ToolCallStatusCompleted), thread: "main"},
		{name: "one byte over the threshold", tool: "edit", input: padJSON(t, StubBodyThreshold+1), status: ptr.Of(core.ToolCallStatusCompleted), thread: "main", wantStub: true},
		{name: "large but nothing to cut (many short strings)", tool: "edit", input: manyShortStrings(t), status: ptr.Of(core.ToolCallStatusCompleted), thread: "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := callBlock(tc.tool, tc.input, "c1")
			opts := &Options{ViewingThreadID: "main", ToolBodies: stub, ToolCallStatusByCallID: map[string]*db.ToolCall{}}
			if tc.status != nil {
				opts.ToolCallStatusByCallID["c1"] = callRow("c1", *tc.status, tc.thread)
			}
			got := contentBlockToProto(b, opts)
			assert.Equal(t, tc.input, *b.ToolInput, "the stored block is never touched")
			if !tc.wantStub {
				assert.Equal(t, tc.input, got.GetInput())
				assert.Nil(t, got.BodyElided)
				assert.Nil(t, got.BodySize)
				return
			}
			assert.True(t, got.GetBodyElided())
			assert.Equal(t, int64(len(tc.input)), got.GetBodySize())
			assert.Less(t, len(got.GetInput()), len(tc.input))
			assert.True(t, json.Valid([]byte(got.GetInput())), "the skeleton of a JSON object is JSON")
		})
	}
}

// padJSON is a JSON object of exactly n bytes whose one string is long
// enough to be cut.
func padJSON(t *testing.T, n int) string {
	t.Helper()
	const shell = `{"file_path":"a","new_string":""}`
	s := `{"file_path":"a","new_string":"` + strings.Repeat("x", n-len(shell)) + `"}`
	require.Len(t, s, n)
	return s
}

func manyShortStrings(t *testing.T) string {
	t.Helper()
	m := map[string]any{}
	for i := range 100 {
		m[fmt.Sprintf("k%03d", i)] = "short value"
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	require.Greater(t, len(b), StubBodyThreshold)
	return string(b)
}

// TestStubToolResultPolicy is the TOOL_RESULT row of the §2 table.
func TestStubToolResultPolicy(t *testing.T) {
	t.Parallel()
	result := func(tool, content string) *db.MessageContentBlock {
		b := &db.MessageContentBlock{
			ID: "r", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: ptr.Of("c1"), Content: ptr.Of(content),
		}
		if tool != "" {
			b.ToolName = ptr.Of(tool)
		}
		return b
	}

	t.Run("a large result becomes a 1 KiB stub, truncated", func(t *testing.T) {
		t.Parallel()
		content := strings.Repeat("log line\n", 1000)
		got := contentBlockToProto(result("shell", content), &Options{ToolBodies: stub})
		assert.LessOrEqual(t, len(got.GetContent()), StubResultPreviewBytes)
		assert.True(t, got.GetTruncated(), "an elided result is truncated, so copy and export fetch it")
		assert.Equal(t, int64(len(content)), got.GetOriginalSize())
		assert.True(t, got.GetBodyElided())
		assert.Equal(t, int64(len(content)), got.GetBodySize())
	})

	t.Run("a result between the stub and display caps is elided too", func(t *testing.T) {
		t.Parallel()
		content := strings.Repeat("z", 4<<10)
		assert.Equal(t, content, contentBlockToProto(result("grep", content), &Options{}).GetContent(), "FULL sends it whole")
		got := contentBlockToProto(result("grep", content), &Options{ToolBodies: stub})
		assert.True(t, got.GetBodyElided())
		assert.True(t, got.GetTruncated())
	})

	t.Run("at the threshold it is sent whole", func(t *testing.T) {
		t.Parallel()
		content := strings.Repeat("z", StubBodyThreshold)
		got := contentBlockToProto(result("grep", content), &Options{ToolBodies: stub})
		assert.Equal(t, content, got.GetContent())
		assert.Nil(t, got.BodyElided)
		assert.Nil(t, got.Truncated)
	})

	t.Run("a shell JSON stub stays valid JSON and keeps exit_code", func(t *testing.T) {
		t.Parallel()
		b, err := json.Marshal(map[string]any{
			"stdout": strings.Repeat("out\n", 5000), "stderr": strings.Repeat("err\n", 5000), "exit_code": 2,
		})
		require.NoError(t, err)
		got := contentBlockToProto(result("shell", string(b)), &Options{ToolBodies: stub})
		require.True(t, got.GetBodyElided())
		assert.LessOrEqual(t, len(got.GetContent()), StubResultPreviewBytes)
		var doc map[string]any
		require.NoError(t, json.Unmarshal([]byte(got.GetContent()), &doc))
		assert.EqualValues(t, 2, doc["exit_code"])
	})

	t.Run("always-full tools are sent whole, named by the call when the result has no name", func(t *testing.T) {
		t.Parallel()
		content := strings.Repeat("task: ", 2000)
		got := contentBlockToProto(result("", content), &Options{ToolBodies: stub, ToolNameByCallID: map[string]string{"c1": "update_task"}})
		assert.Equal(t, content, got.GetContent())
		assert.Nil(t, got.BodyElided)

		// Not elided means sent as FULL sends it: start_run is not a
		// structured result, so over the display cap it is still previewed.
		got = contentBlockToProto(result("start_run", content), &Options{ToolBodies: stub})
		assert.Nil(t, got.BodyElided)
		assert.True(t, proto.Equal(contentBlockToProto(result("start_run", content), &Options{}), got))
	})
}

func TestStubThinkingAndOtherBlocks(t *testing.T) {
	t.Parallel()
	thought := strings.Repeat("hmm ", 10)
	for _, typ := range []reliantv1.ContentBlockType{
		reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING,
		reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING,
	} {
		b := &db.MessageContentBlock{ID: "t", BlockType: typ, Content: ptr.Of(thought)}
		got := contentBlockToProto(b, &Options{ToolBodies: stub})
		assert.Nil(t, got.Content, "%v: thinking of any size is elided", typ)
		assert.True(t, got.GetBodyElided())
		assert.Equal(t, int64(len(thought)), got.GetBodySize())
		assert.Equal(t, thought, *b.Content)
		assert.Equal(t, thought, contentBlockToProto(b, &Options{}).GetContent(), "FULL sends thinking whole")
	}

	empty := &db.MessageContentBlock{ID: "t", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING, ThoughtSignature: ptr.Of("sig")}
	assert.Nil(t, contentBlockToProto(empty, &Options{ToolBodies: stub}).BodyElided, "nothing to elide")

	text := &db.MessageContentBlock{ID: "x", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of(strings.Repeat("words ", 5000))}
	got := contentBlockToProto(text, &Options{ToolBodies: stub})
	assert.Equal(t, *text.Content, got.GetContent(), "text is never elided")
	assert.Nil(t, got.BodyElided)
}

// TestUnsetToolBodyModeIsFull pins v1 at the converter: an Options without
// ToolBodies, and one with FULL, produce byte-identical messages, with every
// input whole and no stub field set.
func TestUnsetToolBodyModeIsFull(t *testing.T) {
	t.Parallel()
	input := bigEditInput(t, 20000)
	blocks := []*db.MessageContentBlock{
		{ID: "a", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING, Content: ptr.Of(strings.Repeat("think ", 1000))},
		callBlock("edit", input, "c1"),
		{ID: "r", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr.Of("c1"), ToolName: ptr.Of("edit"), Content: ptr.Of(strings.Repeat("r", 50000))},
	}
	msg := &db.Message{ID: "m", ChatID: "chat", ThreadID: "main", Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT}
	statuses := map[string]*db.ToolCall{"c1": callRow("c1", core.ToolCallStatusCompleted, "main")}

	marshal := func(mode reliantv1.ToolBodyMode) []byte {
		m := FromDB(msg, blocks, nil, &Options{ToolCallStatusByCallID: statuses, ViewingThreadID: "main", ToolBodies: mode})
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		require.NoError(t, err)
		return b
	}
	assert.Equal(t, marshal(unspecified), marshal(full))

	got := FromDB(msg, blocks, nil, &Options{ToolCallStatusByCallID: statuses, ViewingThreadID: "main"})
	for _, b := range got.ContentBlocks {
		assert.Nil(t, b.BodyElided, b.Id)
		assert.Nil(t, b.BodySize, b.Id)
	}
	assert.Equal(t, input, got.ContentBlocks[1].GetInput())
	assert.Equal(t, strings.Repeat("think ", 1000), got.ContentBlocks[0].GetContent())
	assert.True(t, got.ContentBlocks[2].GetTruncated(), "results are still previewed at the display cap")
	assert.LessOrEqual(t, len(got.ContentBlocks[2].GetContent()), ToolResultPreviewBytes)
}

// TestTranscriptBytesCountsElision: a STUB read's telemetry counts what it
// sent (the kinds) and what it left out (Elided), on both carriers.
func TestTranscriptBytesCountsElision(t *testing.T) {
	t.Parallel()
	msgs := []*reliantv1.Message{{ContentBlocks: []*reliantv1.ContentBlock{
		{Type: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, Input: ptr.Of("skel"), BodyElided: ptr.Of(true), BodySize: ptr.Of(int64(100))},
		{Type: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, Content: ptr.Of("stub!"), BodyElided: ptr.Of(true), BodySize: ptr.Of(int64(50))},
		{Type: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING, BodyElided: ptr.Of(true), BodySize: ptr.Of(int64(7))},
		{Type: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("not elided")},
	}}}
	sent := TranscriptBytes{ToolBodies: stub}
	sent.AddMessages(msgs...)
	assert.Equal(t, BlockBytes{Blocks: 3, Bytes: 96 + 45 + 7}, sent.Elided)
	assert.Equal(t, BlockBytes{Blocks: 1, Bytes: 4}, sent.ToolInput, "the kinds count what was sent")
	assert.Equal(t, BlockBytes{Blocks: 1}, sent.Thinking)

	attrs := map[string]any{}
	for _, kv := range sent.Attributes("transcript") {
		attrs[string(kv.Key)] = kv.Value.AsInterface()
	}
	assert.Equal(t, int64(3), attrs["transcript.blocks_elided"])
	assert.Equal(t, int64(148), attrs["transcript.bytes_elided"])
	assert.Equal(t, "TOOL_BODY_MODE_STUB", attrs["transcript.tool_bodies"])

	var unset TranscriptBytes
	for _, kv := range unset.Attributes("transcript") {
		assert.NotEqual(t, "transcript.tool_bodies", string(kv.Key), "no mode set, none recorded")
	}

	var update TranscriptBytes
	update.AddChatUpdate(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
		`{"message":{"content_blocks":[{"type":3,"content":"stub","truncated":true,"body_elided":true,"body_size":40},{"type":5,"body_elided":true,"body_size":9}]}}`)
	assert.Equal(t, BlockBytes{Blocks: 2, Bytes: 36 + 9}, update.Elided)
}

func TestInputSkeleton(t *testing.T) {
	t.Parallel()
	skeleton := func(t *testing.T, v any) map[string]any {
		t.Helper()
		b, err := json.Marshal(v)
		require.NoError(t, err)
		out, cut := InputSkeleton(string(b))
		require.True(t, cut)
		dec := json.NewDecoder(strings.NewReader(out))
		dec.UseNumber()
		var doc map[string]any
		require.NoError(t, dec.Decode(&doc))
		return doc
	}
	long := strings.Repeat("x", 3000)

	t.Run("short strings, numbers, booleans and null stay", func(t *testing.T) {
		t.Parallel()
		doc := skeleton(t, map[string]any{
			"file_path": "a/b.go", "offset": 9007199254740993, "replace_all": true, "x": nil, "content": long,
		})
		assert.Equal(t, "a/b.go", doc["file_path"])
		assert.Equal(t, json.Number("9007199254740993"), doc["offset"])
		assert.Equal(t, true, doc["replace_all"])
		assert.Contains(t, doc, "x")
		assert.Equal(t, strings.Repeat("x", skeletonCutString)+skeletonEllipsis, doc["content"])
	})

	t.Run("a string at the keep limit stays whole", func(t *testing.T) {
		t.Parallel()
		at := strings.Repeat("y", skeletonKeepString)
		doc := skeleton(t, map[string]any{"description": at, "content": long})
		assert.Equal(t, at, doc["description"])
	})

	t.Run("cuts land on a rune boundary", func(t *testing.T) {
		t.Parallel()
		doc := skeleton(t, map[string]any{"content": "a" + strings.Repeat("é", 2000)})
		s := doc["content"].(string)
		assert.True(t, utf8.ValidString(s))
		assert.LessOrEqual(t, len(strings.TrimSuffix(s, skeletonEllipsis)), skeletonCutString)
	})

	t.Run("command keeps 4 KiB, so a deploy behind a long chain is still found", func(t *testing.T) {
		t.Parallel()
		cmd := strings.Repeat("echo step && ", 250) + "forge env deploy staging"
		require.Less(t, len(cmd), skeletonKeepCommand)
		doc := skeleton(t, map[string]any{"command": cmd, "description": long})
		assert.Equal(t, cmd, doc["command"])

		huge := strings.Repeat("c", 10000)
		doc = skeleton(t, map[string]any{"command": huge})
		assert.Equal(t, strings.Repeat("c", skeletonKeepCommand)+skeletonEllipsis, doc["command"])
	})

	t.Run("patch_text keeps exactly its header lines", func(t *testing.T) {
		t.Parallel()
		patch := "*** Begin Patch\n*** Update File: a.go\n@@\n-" + long + "\n+new\n*** Add File: b.go\n+" + long +
			"\n*** Delete File: c.go\n*** Update File: d.go\n*** Move to: e.go\n@@\n-x\n+y\n*** End Patch"
		doc := skeleton(t, map[string]any{"patch_text": patch})
		assert.Equal(t, "*** Begin Patch\n*** Update File: a.go\n*** Add File: b.go\n*** Delete File: c.go\n*** Update File: d.go\n*** Move to: e.go\n*** End Patch", doc["patch_text"])
	})

	t.Run("message keeps its first line", func(t *testing.T) {
		t.Parallel()
		first := "feat(chat): " + strings.Repeat("a long subject ", 25)
		require.Greater(t, len(first), skeletonCutString)
		doc := skeleton(t, map[string]any{"message": first + "\n\n" + long})
		assert.Equal(t, first+skeletonEllipsis, doc["message"])

		doc = skeleton(t, map[string]any{"message": "short subject\n\n" + long})
		assert.Equal(t, cutHead("short subject\n\n"+long, skeletonCutString)+skeletonEllipsis, doc["message"])
	})

	t.Run("arrays keep their first elements, each walked", func(t *testing.T) {
		t.Parallel()
		edits := make([]any, 250)
		for i := range edits {
			edits[i] = map[string]any{"file_path": fmt.Sprintf("f%d.go", i), "old_string": "o", "new_string": "n"}
		}
		doc := skeleton(t, map[string]any{"edits": edits})
		kept := doc["edits"].([]any)
		require.Len(t, kept, skeletonKeepArray)
		assert.Equal(t, "f199.go", kept[199].(map[string]any)["file_path"])

		doc = skeleton(t, map[string]any{"edits": []any{map[string]any{"file_path": "a.go", "new_string": long}}})
		edit := doc["edits"].([]any)[0].(map[string]any)
		assert.Equal(t, "a.go", edit["file_path"])
		assert.Equal(t, strings.Repeat("x", skeletonCutString)+skeletonEllipsis, edit["new_string"])
	})

	t.Run("an input that is not a JSON object is head-cut", func(t *testing.T) {
		t.Parallel()
		out, cut := InputSkeleton(strings.Repeat("é", 1000))
		assert.True(t, cut)
		assert.True(t, utf8.ValidString(out))
		assert.LessOrEqual(t, len(out), skeletonCutNonJSON)

		out, cut = InputSkeleton(`[1,2,3]`)
		assert.False(t, cut)
		assert.Equal(t, `[1,2,3]`, out)
	})

	t.Run("nothing cut returns the input itself", func(t *testing.T) {
		t.Parallel()
		in := `{ "b": "x",  "a": 1 }`
		out, cut := InputSkeleton(in)
		assert.False(t, cut)
		assert.Equal(t, in, out)
	})
}

// TestPreviewMessageUpdateStub: a replayed update under STUB stubs results
// and elides thinking, and keeps tool-call inputs whole (it cannot tell a
// finished call from a running one).
func TestPreviewMessageUpdateStub(t *testing.T) {
	t.Parallel()
	input := bigEditInput(t, 5000)
	result := strings.Repeat("result line\n", 1000)
	raw, err := json.Marshal(map[string]any{"message": map[string]any{
		"id": "m", "seq": int64(9007199254740993),
		"content_blocks": []any{
			map[string]any{"id": "t", "type": int(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING), "content": "short thought"},
			map[string]any{"id": "c", "type": int(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL), "tool_name": "edit", "input": input},
			map[string]any{"id": "r", "type": int(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT), "tool_name": "edit", "content": result},
			map[string]any{"id": "p", "type": int(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT), "tool_name": "list_tasks", "content": result},
		},
	}})
	require.NoError(t, err)

	out := PreviewMessageUpdate(raw, stub)
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	var decoded map[string]any
	require.NoError(t, dec.Decode(&decoded))
	msg := decoded["message"].(map[string]any)
	assert.Equal(t, json.Number("9007199254740993"), msg["seq"])
	blocks := msg["content_blocks"].([]any)

	thinking := blocks[0].(map[string]any)
	assert.NotContains(t, thinking, "content")
	assert.Equal(t, true, thinking["body_elided"])
	assert.Equal(t, json.Number("13"), thinking["body_size"])

	call := blocks[1].(map[string]any)
	assert.Equal(t, input, call["input"], "replay keeps inputs whole")
	assert.NotContains(t, call, "body_elided")

	res := blocks[2].(map[string]any)
	assert.LessOrEqual(t, len(res["content"].(string)), StubResultPreviewBytes)
	assert.Equal(t, true, res["truncated"])
	assert.Equal(t, true, res["body_elided"])
	assert.Equal(t, json.Number(fmt.Sprint(len(result))), res["original_size"])
	assert.Equal(t, json.Number(fmt.Sprint(len(result))), res["body_size"])

	plan := blocks[3].(map[string]any)
	assert.Equal(t, result, plan["content"], "always-full tools are whole")

	// FULL (and unset) is the original preview: no stub fields at all.
	for _, mode := range []reliantv1.ToolBodyMode{unspecified, full} {
		outFull := string(PreviewMessageUpdate(raw, mode))
		assert.NotContains(t, outFull, "body_elided")
		assert.Contains(t, outFull, "short thought")
	}

	small := []byte(`{"id":"m","content_blocks":[{"type":1,"content":"ok"}]}`)
	assert.Equal(t, small, PreviewMessageUpdate(small, stub), "nothing to elide: returned as is")
}
