// Copyright (c) 2025 Reliant Labs

package messageproto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func protoBlock(bt reliantv1.ContentBlockType, content, input string) *reliantv1.ContentBlock {
	b := &reliantv1.ContentBlock{Id: "b", Type: bt}
	if content != "" {
		b.Content = ptr.Of(content)
	}
	if input != "" {
		b.Input = ptr.Of(input)
	}
	return b
}

func TestTranscriptBytes_AddMessagesSplitsBodiesByKind(t *testing.T) {
	m1 := &reliantv1.Message{Id: "m1", ContentBlocks: []*reliantv1.ContentBlock{
		protoBlock(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, "hello", ""),
		protoBlock(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING, "pondering", ""),
		protoBlock(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING, "ciphertext", ""),
		protoBlock(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, "", `{"command":"ls"}`),
	}}
	m2 := &reliantv1.Message{Id: "m2", ContentBlocks: []*reliantv1.ContentBlock{
		protoBlock(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, "file1\nfile2", ""),
		protoBlock(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, "attachment-id", ""),
		protoBlock(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_UNSPECIFIED, "", ""),
	}}

	var got TranscriptBytes
	got.AddMessages(m1, nil, m2)

	assert.Equal(t, 2, got.Messages, "a nil message is not counted")
	assert.Equal(t, int64(proto.Size(m1)+proto.Size(m2)), got.Bytes, "bytes is the wire size of the messages")
	assert.Equal(t, BlockBytes{Blocks: 1, Bytes: 5}, got.Text)
	assert.Equal(t, BlockBytes{Blocks: 2, Bytes: int64(len("pondering") + len("ciphertext"))}, got.Thinking,
		"redacted thinking counts as thinking")
	assert.Equal(t, BlockBytes{Blocks: 1, Bytes: int64(len(`{"command":"ls"}`))}, got.ToolInput)
	assert.Equal(t, BlockBytes{Blocks: 1, Bytes: int64(len("file1\nfile2"))}, got.ToolResult)
	assert.Equal(t, BlockBytes{Blocks: 2, Bytes: int64(len("attachment-id"))}, got.Other,
		"images and unknown kinds count as other, an empty block with zero bytes")
	assert.Equal(t, 7, got.Blocks())
	sum := got.Text.Bytes + got.ToolInput.Bytes + got.ToolResult.Bytes + got.Thinking.Bytes + got.Other.Bytes
	assert.Greater(t, got.Bytes, sum, "the wire size includes ids and framing besides the bodies")
}

// A tool result counts as what is sent, not as what is stored: the read
// paths measure after the 8 KiB preview.
func TestTranscriptBytes_CountsATruncatedResultAsItsPreview(t *testing.T) {
	stored := strings.Repeat("line of output\n", 4000)
	msg := FromDB(&db.Message{ID: "m", CreatedAt: time.Now(), UpdatedAt: time.Now()}, []*db.MessageContentBlock{{
		ID:        "r",
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
		ToolName:  ptr.Of("shell"),
		Content:   &stored,
	}}, nil, nil)
	require.True(t, msg.ContentBlocks[0].GetTruncated())

	var got TranscriptBytes
	got.AddMessages(msg)

	assert.Equal(t, int64(len(msg.ContentBlocks[0].GetContent())), got.ToolResult.Bytes)
	assert.LessOrEqual(t, got.ToolResult.Bytes, int64(ToolResultPreviewBytes))
}

func TestTranscriptBytes_AddChatUpdate(t *testing.T) {
	message := reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE

	t.Run("wrapped payload with numeric types", func(t *testing.T) {
		blocks := db.ContentBlockPayloadsWithToolCalls([]*db.MessageContentBlock{
			{ID: "t", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("hi")},
			{ID: "c", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, ToolInput: ptr.Of(`{"a":1}`), ToolCallID: ptr.Of("call")},
			{ID: "r", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, Content: ptr.Of("done"), ToolCallID: ptr.Of("call")},
			{ID: "k", BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING, Content: ptr.Of("hmm")},
		}, nil)
		raw, err := json.Marshal(map[string]any{"message": map[string]any{"id": "m", "content_blocks": blocks}})
		require.NoError(t, err)

		var got TranscriptBytes
		got.AddChatUpdate(message, string(raw))

		assert.Equal(t, 1, got.Messages)
		assert.Equal(t, int64(len(raw)), got.Bytes)
		assert.Equal(t, BlockBytes{Blocks: 1, Bytes: 2}, got.Text)
		// The input's quotes are escaped on the wire: {\"a\":1}.
		assert.Equal(t, BlockBytes{Blocks: 1, Bytes: int64(len(`{\"a\":1}`))}, got.ToolInput)
		assert.Equal(t, BlockBytes{Blocks: 1, Bytes: 4}, got.ToolResult)
		assert.Equal(t, BlockBytes{Blocks: 1, Bytes: 3}, got.Thinking)
		assert.Equal(t, BlockBytes{}, got.Other)
	})

	t.Run("bare payload with enum-name types", func(t *testing.T) {
		raw := `{"content_blocks":[{"type":"CONTENT_BLOCK_TYPE_TEXT","content":"abc"},{"type":"CONTENT_BLOCK_TYPE_REDACTED_THINKING","content":"xy"},{"type":"NOT_A_TYPE","content":"z"},{"type":4,"content":null}]}`
		var got TranscriptBytes
		got.AddChatUpdate(message, raw)

		assert.Equal(t, BlockBytes{Blocks: 1, Bytes: 3}, got.Text)
		assert.Equal(t, BlockBytes{Blocks: 1, Bytes: 2}, got.Thinking)
		assert.Equal(t, BlockBytes{Blocks: 2, Bytes: 1}, got.Other, "an unknown name and a null body count as other")
	})

	t.Run("updates that are not messages carry no transcript", func(t *testing.T) {
		var got TranscriptBytes
		got.AddChatUpdate(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAMING_DELTA, `{"content_blocks":[{"type":1,"content":"x"}]}`)
		assert.Equal(t, TranscriptBytes{}, got)
	})

	t.Run("a payload that does not parse is a message with no blocks", func(t *testing.T) {
		var got TranscriptBytes
		got.AddChatUpdate(message, `{not json`)
		assert.Equal(t, TranscriptBytes{Messages: 1, Bytes: int64(len(`{not json`))}, got)
	})

	t.Run("updates accumulate", func(t *testing.T) {
		var got TranscriptBytes
		got.AddChatUpdate(message, `{"message":{"content_blocks":[{"type":1,"content":"ab"}]}}`)
		got.AddChatUpdate(message, `{"message":{"content_blocks":[{"type":1,"content":"cde"}]}}`)
		assert.Equal(t, 2, got.Messages)
		assert.Equal(t, BlockBytes{Blocks: 2, Bytes: 5}, got.Text)
	})
}

func TestTranscriptBytes_Attributes(t *testing.T) {
	tb := TranscriptBytes{
		Messages:   3,
		Bytes:      1000,
		Text:       BlockBytes{Blocks: 2, Bytes: 10},
		ToolInput:  BlockBytes{Blocks: 1, Bytes: 20},
		ToolResult: BlockBytes{Blocks: 1, Bytes: 30},
		Thinking:   BlockBytes{Blocks: 1, Bytes: 40},
		Other:      BlockBytes{Blocks: 1, Bytes: 50},
	}
	got := map[attribute.Key]int64{}
	for _, kv := range tb.Attributes("transcript.live") {
		got[kv.Key] = kv.Value.AsInt64()
	}
	assert.Equal(t, map[attribute.Key]int64{
		"transcript.live.messages":           3,
		"transcript.live.bytes":              1000,
		"transcript.live.blocks":             6,
		"transcript.live.text_bytes":         10,
		"transcript.live.text_blocks":        2,
		"transcript.live.tool_input_bytes":   20,
		"transcript.live.tool_input_blocks":  1,
		"transcript.live.tool_result_bytes":  30,
		"transcript.live.tool_result_blocks": 1,
		"transcript.live.thinking_bytes":     40,
		"transcript.live.thinking_blocks":    1,
		"transcript.live.other_bytes":        50,
		"transcript.live.other_blocks":       1,
	}, got)
}
