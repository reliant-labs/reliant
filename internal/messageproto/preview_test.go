// Copyright (c) 2025 Reliant Labs

package messageproto

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestPreviewToolResult_SmallContentIsSentWhole(t *testing.T) {
	content := strings.Repeat("x", ToolResultPreviewBytes)
	got, truncated := PreviewToolResult("shell", content)
	assert.False(t, truncated)
	assert.Equal(t, content, got)
}

func TestPreviewToolResult_KeepsHeadAndTailWithinTheCap(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() < 200<<10; i++ {
		b.WriteString("line of test output\n")
	}
	b.WriteString("FAIL: TestTheOneThatMatters\n")
	content := "first line\n" + b.String()

	got, truncated := PreviewToolResult("shell", content)
	require.True(t, truncated)
	assert.LessOrEqual(t, len(got), ToolResultPreviewBytes)
	assert.True(t, strings.HasPrefix(got, "first line\n"), "the head is kept")
	assert.True(t, strings.HasSuffix(got, "FAIL: TestTheOneThatMatters\n"), "the tail is kept: that is where a run's conclusion is")
	assert.Contains(t, got, "KB omitted")
	assert.NotContains(t, omissionMarker(1<<20), ".", "the marker must not read as a file path to the glob renderer")
	assert.NotContains(t, omissionMarker(1<<20), "/", "the marker must not read as a file path to the glob renderer")
}

func TestPreviewToolResult_CutsOnALineBoundary(t *testing.T) {
	content := strings.Repeat("abcdefghij0123456789\n", 2000)
	got, truncated := PreviewToolResult("grep", content)
	require.True(t, truncated)
	head, rest, found := strings.Cut(got, "\n… ")
	require.True(t, found, "preview has no omission marker")
	_, tail, found := strings.Cut(rest, " omitted …\n")
	require.True(t, found, "preview has no omission marker")
	for _, line := range strings.Split(head, "\n") {
		assert.Equal(t, "abcdefghij0123456789", line, "a head line was cut in half")
	}
	for _, line := range strings.Split(strings.TrimSuffix(tail, "\n"), "\n") {
		assert.Equal(t, "abcdefghij0123456789", line, "a tail line was cut in half")
	}
}

func TestPreviewToolResult_CutsOnAUTF8Boundary(t *testing.T) {
	// No newlines, so the cut cannot snap to a line; every rune is 3 or 4
	// bytes, so almost any byte offset lands inside one.
	content := strings.Repeat("日本語🙂", 4000)
	// An ASCII prefix of each length shifts where every rune falls relative
	// to the cut points.
	for _, offset := range []int{0, 1, 2, 3} {
		got, truncated := PreviewToolResult("fetch", strings.Repeat("a", offset)+content)
		require.True(t, truncated)
		assert.True(t, utf8.ValidString(got), "offset %d: preview is not valid UTF-8", offset)
		assert.LessOrEqual(t, len(got), ToolResultPreviewBytes)
	}
}

func TestPreviewToolResult_JSONObjectStaysValidJSON(t *testing.T) {
	// The shell tool's structured output: the client parses it, so the
	// preview must still parse, with its long strings shortened.
	stdout := strings.Repeat("compiling package\n", 20000) + "BUILD FAILED\n"
	content, err := json.Marshal(map[string]any{
		"stdout":    stdout,
		"stderr":    "warning: <deprecated> & friends",
		"exit_code": 2,
	})
	require.NoError(t, err)

	got, truncated := PreviewToolResult("shell", string(content))
	require.True(t, truncated)
	assert.LessOrEqual(t, len(got), ToolResultPreviewBytes)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &parsed), "a JSON result's preview must still parse")
	assert.Equal(t, float64(2), parsed["exit_code"])
	assert.Equal(t, "warning: <deprecated> & friends", parsed["stderr"], "short fields are untouched")
	previewStdout, _ := parsed["stdout"].(string)
	assert.True(t, strings.HasPrefix(previewStdout, "compiling package\n"))
	assert.True(t, strings.HasSuffix(previewStdout, "BUILD FAILED\n"))
	assert.Contains(t, previewStdout, "omitted")
}

func TestPreviewToolResult_JSONWithoutLongStringsFallsBackToText(t *testing.T) {
	items := make([]int, 10000)
	content, err := json.Marshal(map[string]any{"items": items})
	require.NoError(t, err)

	got, truncated := PreviewToolResult("code_context", string(content))
	require.True(t, truncated)
	assert.LessOrEqual(t, len(got), ToolResultPreviewBytes)
	assert.Contains(t, got, "omitted")
}

func TestPreviewToolResult_StructuredToolsAreNeverTruncated(t *testing.T) {
	// The tasks sidebar parses these results to build the plan; a preview
	// would build the wrong plan rather than render a shorter one.
	big := `{"plan":"` + strings.Repeat("p", 3*ToolResultPreviewBytes) + `"}`
	for _, name := range []string{"create_plan", "update_task", "add_task", "create_subtask", "list_tasks", "get_plan", "update_plan", "ask_user", "request_machine", "mcp__reliant__create_plan", "LIST_TASKS"} {
		got, truncated := PreviewToolResult(name, big)
		assert.False(t, truncated, name)
		assert.Equal(t, big, got, name)
	}
}

// staticToolCalls is a Reader over fixed tool-call rows and no threads.
type staticToolCalls []*db.ToolCall

func (s staticToolCalls) ListToolCallsByMessageIDs(context.Context, []string) ([]*db.ToolCall, error) {
	return s, nil
}

func (s staticToolCalls) ListToolCallsByIDs(context.Context, []string) ([]*db.ToolCall, error) {
	return nil, nil
}

func (s staticToolCalls) ListThreadsByIDs(context.Context, []string) ([]*db.Thread, error) {
	return nil, nil
}

func TestAssembleForDisplay_ResultIsSentOnceAndPreviewed(t *testing.T) {
	now := time.Now().UTC()
	big := strings.Repeat("a long line of output\n", 5000)
	small := "ok"
	planResult := strings.Repeat("- task\n", 3000)

	assistant := &db.Message{ID: "m1", ChatID: "c1", Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, CreatedAt: now, UpdatedAt: now}
	tool := &db.Message{ID: "m2", ChatID: "c1", Role: reliantv1.MessageRole_MESSAGE_ROLE_TOOL, CreatedAt: now, UpdatedAt: now}
	call := func(id, name, input string) *db.MessageContentBlock {
		return &db.MessageContentBlock{ID: "b-" + id, MessageID: "m1", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, ToolCallID: ptr.Of(id), ToolName: ptr.Of(name), ToolInput: ptr.Of(input)}
	}
	result := func(id string, name *string, content string) *db.MessageContentBlock {
		return &db.MessageContentBlock{ID: "r-" + id, MessageID: "m2", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr.Of(id), ToolName: name, Content: ptr.Of(content), IsError: ptr.Of(false)}
	}
	// A large write input is the diff the UI renders: never shortened.
	bigInput := `{"path":"a.go","content":"` + strings.Repeat("x", 3*ToolResultPreviewBytes) + `"}`
	blocks := map[string][]*db.MessageContentBlock{
		"m1": {call("big", "shell", `{"command":"go test ./..."}`), call("small", "shell", `{}`), call("plan", "create_plan", `{}`), call("write", "write", bigInput)},
		// The plan result carries no tool_name of its own: the exemption
		// must still find it through the call.
		"m2": {result("big", ptr.Of("shell"), big), result("small", ptr.Of("shell"), small), result("plan", nil, planResult), result("write", ptr.Of("write"), small)},
	}

	msgs := AssembleForDisplay(context.Background(), staticToolCalls{}, []*db.Message{assistant, tool}, blocks, nil, "", 0)
	require.Len(t, msgs, 2)

	for _, b := range msgs[0].ContentBlocks {
		assert.Nil(t, b.Content, "a tool_call block does not carry its result: the client joins by tool_call_id")
		assert.Nil(t, b.Truncated)
		if b.GetToolCallId() == "write" {
			assert.Equal(t, bigInput, b.GetInput(), "tool inputs are never shortened")
		}
	}

	byID := map[string]*reliantv1.ContentBlock{}
	for _, b := range msgs[1].ContentBlocks {
		byID[b.GetToolCallId()] = b
	}
	assert.True(t, byID["big"].GetTruncated())
	assert.Equal(t, int64(len(big)), byID["big"].GetOriginalSize())
	assert.LessOrEqual(t, len(byID["big"].GetContent()), ToolResultPreviewBytes)
	assert.Equal(t, small, byID["small"].GetContent())
	assert.Nil(t, byID["small"].Truncated)
	assert.Nil(t, byID["small"].OriginalSize)
	assert.Equal(t, planResult, byID["plan"].GetContent(), "a structured result is sent whole even without its own tool_name")
	assert.False(t, byID["plan"].GetTruncated())

	// The stored block is untouched: truncation is a wire concern.
	assert.Equal(t, big, *blocks["m2"][0].Content)

	// And the result is on the wire once, not twice.
	wire, err := proto.Marshal(&reliantv1.ListMessagesResponse{Messages: msgs})
	require.NoError(t, err)
	assert.Less(t, len(wire), len(big)/2)
}

func TestFromDB_ExemptionFallsBackToTheDurableToolCallRow(t *testing.T) {
	now := time.Now().UTC()
	content := strings.Repeat("- task\n", 3000)
	msg := &db.Message{ID: "m", Role: reliantv1.MessageRole_MESSAGE_ROLE_TOOL, CreatedAt: now, UpdatedAt: now}
	block := &db.MessageContentBlock{ID: "r", MessageID: "m", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, ToolCallID: ptr.Of("c"), Content: ptr.Of(content)}

	got := FromDB(msg, []*db.MessageContentBlock{block}, nil, &Options{
		ToolCallStatusByCallID: map[string]*db.ToolCall{"c": {ID: "c", ToolName: "list_tasks"}},
	})
	assert.Equal(t, content, got.ContentBlocks[0].GetContent())

	got = FromDB(msg, []*db.MessageContentBlock{block}, nil, nil)
	assert.True(t, got.ContentBlocks[0].GetTruncated(), "with no name to exempt it, a large result is previewed")
}

func TestPreviewMessageUpdate(t *testing.T) {
	big := strings.Repeat("output line\n", 4000)
	update := func(wrapped bool) []byte {
		msg := map[string]any{
			"id":  "msg-1",
			"seq": int64(9007199254740993), // > 2^53: must survive re-marshaling exactly
			"content_blocks": []any{
				map[string]any{"id": "b1", "type": int(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT), "tool_call_id": "c1", "tool_name": "shell", "content": big},
				map[string]any{"id": "b2", "type": int(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT), "tool_call_id": "c2", "tool_name": "create_plan", "content": big},
				map[string]any{"id": "b3", "type": int(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT), "content": big},
			},
		}
		var payload any = msg
		if wrapped {
			payload = map[string]any{"message": msg}
		}
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		return raw
	}

	for _, wrapped := range []bool{false, true} {
		out := PreviewMessageUpdate(update(wrapped))
		var decoded map[string]any
		dec := json.NewDecoder(strings.NewReader(string(out)))
		dec.UseNumber()
		require.NoError(t, dec.Decode(&decoded))
		msg := decoded
		if wrapped {
			msg = decoded["message"].(map[string]any)
		}
		assert.Equal(t, json.Number("9007199254740993"), msg["seq"])
		blocks := msg["content_blocks"].([]any)

		shell := blocks[0].(map[string]any)
		assert.Equal(t, true, shell["truncated"])
		assert.Equal(t, json.Number("48000"), shell["original_size"])
		assert.LessOrEqual(t, len(shell["content"].(string)), ToolResultPreviewBytes)

		plan := blocks[1].(map[string]any)
		assert.Equal(t, big, plan["content"], "structured results are sent whole")
		assert.NotContains(t, plan, "truncated")

		text := blocks[2].(map[string]any)
		assert.Equal(t, big, text["content"], "only tool results are previewed")
	}

	small := []byte(`{"id":"m","content_blocks":[{"type":3,"content":"ok"}]}`)
	assert.Equal(t, small, PreviewMessageUpdate(small), "a small update is returned as is")
	assert.Equal(t, []byte(`not json`+strings.Repeat(" ", ToolResultPreviewBytes)+`"content_blocks"`), PreviewMessageUpdate([]byte(`not json`+strings.Repeat(" ", ToolResultPreviewBytes)+`"content_blocks"`)))
}
