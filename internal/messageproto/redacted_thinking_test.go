// Copyright (c) 2026 Reliant Labs

package messageproto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

const sealedCiphertext = "EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlxh0L5L8rLVyIwxtE3rAFBa8cr3qpPkNRj2YfWXGmKDxH4mUcR0FnMPF3A=="

// A redacted_thinking block reaches the client with its type and position and
// an empty content, in every tool-body mode, and the stored block keeps its
// ciphertext for the provider replay.
func TestRedactedThinkingIsSentBlank(t *testing.T) {
	t.Parallel()
	sealed := &db.MessageContentBlock{
		ID: "sealed", Position: 0,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING,
		Content:   ptr.Of(sealedCiphertext),
	}
	text := &db.MessageContentBlock{
		ID: "text", Position: 1,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   ptr.Of("Here is the answer."),
	}
	msg := &db.Message{ID: "m", ChatID: "chat", ThreadID: "main", Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT}

	for _, mode := range []reliantv1.ToolBodyMode{unspecified, full, stub} {
		got := FromDB(msg, []*db.MessageContentBlock{sealed, text}, nil, &Options{ToolBodies: mode})
		require.Len(t, got.ContentBlocks, 2, "mode %v", mode)
		block := got.ContentBlocks[0]
		assert.Equal(t, "sealed", block.GetId())
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING, block.GetType())
		assert.EqualValues(t, 0, block.GetIndex())
		assert.Equal(t, ptr.Of(""), block.Content, "mode %v", mode)
		assert.Nil(t, block.BodyElided, "mode %v", mode)
		assert.Equal(t, "Here is the answer.", got.ContentBlocks[1].GetContent(), "other blocks are unchanged")
		assert.Equal(t, reliantv1.StreamingState_STREAMING_STATE_COMPLETE, got.GetStreamingState(),
			"streaming state is read from the stored blocks, not the blanked ones")
	}
	assert.Equal(t, sealedCiphertext, *sealed.Content, "the stored block is not touched")
}

// A live or replayed message update blanks a redacted_thinking block's
// content in every mode: new rows are written blank (db.ProviderOnlyContent),
// and rows written before that still carry the ciphertext.
func TestPreviewMessageUpdateBlanksRedactedThinking(t *testing.T) {
	t.Parallel()
	sealedType := reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING
	for name, typ := range map[string]any{"number": int(sealedType), "name": sealedType.String()} {
		raw, err := json.Marshal(map[string]any{"message": map[string]any{
			"id": "m", "seq": int64(9007199254740993),
			"content_blocks": []any{
				map[string]any{"id": "s", "type": typ, "index": 0, "content": sealedCiphertext},
				map[string]any{"id": "x", "type": int(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT), "index": 1, "content": "answer"},
			},
		}})
		require.NoError(t, err)
		require.Less(t, len(raw), StubBodyThreshold, "small enough for every fast path")

		for _, mode := range []reliantv1.ToolBodyMode{unspecified, full, stub} {
			out := PreviewMessageUpdate(raw, mode)
			assert.NotContains(t, string(out), sealedCiphertext, "%s, mode %v", name, mode)

			dec := json.NewDecoder(strings.NewReader(string(out)))
			dec.UseNumber()
			var decoded map[string]any
			require.NoError(t, dec.Decode(&decoded))
			msg := decoded["message"].(map[string]any)
			assert.Equal(t, json.Number("9007199254740993"), msg["seq"])
			blocks := msg["content_blocks"].([]any)
			require.Len(t, blocks, 2)
			sealed := blocks[0].(map[string]any)
			assert.Equal(t, "s", sealed["id"])
			assert.Equal(t, "", sealed["content"], "%s, mode %v", name, mode)
			assert.NotContains(t, sealed, "body_elided")
			assert.Equal(t, "answer", blocks[1].(map[string]any)["content"])
		}
	}

	blank := []byte(`{"id":"m","content_blocks":[{"type":8,"content":""}]}`)
	assert.Equal(t, blank, PreviewMessageUpdate(blank, full), "already blank: returned as is")
	assert.Equal(t, blank, PreviewMessageUpdate(blank, stub), "already blank: returned as is")
}
