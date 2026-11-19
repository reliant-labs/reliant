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

// sealedContents decodes a message update as a client would and returns the
// content of every block in either block list that may name
// REDACTED_THINKING, so a test can require each one blank however the
// payload spelled it.
func sealedContents(t *testing.T, out []byte) []any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	var payload map[string]any
	require.NoError(t, dec.Decode(&payload), "%s", out)
	var contents []any
	for _, blocks := range blockLists(payload) {
		for _, b := range blocks {
			block, ok := b.(map[string]any)
			if !ok || !isSealedBlockType(block["type"]) {
				continue
			}
			if content, present := block["content"]; present {
				contents = append(contents, content)
			}
		}
	}
	return contents
}

// B1: a stored update whose JSON puts whitespace after the colon ("type": 8)
// is as valid as the compact one, and a short one used to skip the decode and
// ship the ciphertext.
func TestPreviewMessageUpdateBlanksSpacedRedactedThinking(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"space":   `{"id":"m","content_blocks":[{"id":"s","type": 8,"content":"` + sealedCiphertext + `"}]}`,
		"newline": "{\"id\":\"m\",\"content_blocks\":[{\"id\":\"s\",\"type\":\n8,\"content\":\"" + sealedCiphertext + "\"}]}",
		"tab":     "{\"id\":\"m\",\"content_blocks\":[{\"id\":\"s\",\"type\"\t:\t8,\"content\":\"" + sealedCiphertext + "\"}]}",
	} {
		require.Less(t, len(raw), StubBodyThreshold, name)
		for _, mode := range []reliantv1.ToolBodyMode{unspecified, full, stub} {
			out := PreviewMessageUpdate([]byte(raw), mode)
			assert.NotContains(t, string(out), sealedCiphertext, "%s, mode %v", name, mode)
			assert.Equal(t, []any{""}, sealedContents(t, out), "%s, mode %v", name, mode)
		}
	}
}

// Every JSON spelling of a redacted_thinking block a client could parse as
// one comes out blank: whitespace, number forms, the enum name in its
// spellings, escaped keys and values, duplicate keys, the block at the top
// level or under "message", and an indented payload.
func TestPreviewMessageUpdateBlanksRedactedThinkingInAnySpelling(t *testing.T) {
	t.Parallel()
	const c = sealedCiphertext
	blocks := func(block string) string { return `{"id":"m","content_blocks":[` + block + `]}` }
	spellings := map[string]string{
		"compact":              blocks(`{"type":8,"content":"` + c + `"}`),
		"space before colon":   blocks(`{"type" :8,"content":"` + c + `"}`),
		"spaces around colon":  blocks(`{ "type" : 8 , "content" : "` + c + `" }`),
		"crlf":                 blocks("{\"type\":\r\n8,\"content\":\r\n\"" + c + "\"}"),
		"8.0":                  blocks(`{"type":8.0,"content":"` + c + `"}`),
		"8e0":                  blocks(`{"type":8e0,"content":"` + c + `"}`),
		"80e-1":                blocks(`{"type":80e-1,"content":"` + c + `"}`),
		"0.8E+1":               blocks(`{"type":0.8E+1,"content":"` + c + `"}`),
		"name":                 blocks(`{"type":"CONTENT_BLOCK_TYPE_REDACTED_THINKING","content":"` + c + `"}`),
		"name, spaced":         blocks(`{"type" : "CONTENT_BLOCK_TYPE_REDACTED_THINKING" ,"content":"` + c + `"}`),
		"name, escaped":        blocks(`{"type":"CONTENT_BLOCK_TYPE_REDACTED\u005fTHINKING","content":"` + c + `"}`),
		"name, lower case":     blocks(`{"type":"content_block_type_redacted_thinking","content":"` + c + `"}`),
		"name, short":          blocks(`{"type":"REDACTED_THINKING","content":"` + c + `"}`),
		"numeric string":       blocks(`{"type":"8","content":"` + c + `"}`),
		"escaped type key":     blocks(`{"\u0074ype":8,"content":"` + c + `"}`),
		"escaped content key":  blocks(`{"type":8,"\u0063ontent":"` + c + `"}`),
		"escaped blocks key":   `{"id":"m","content\u005fblocks":[{"type":8,"content":"` + c + `"}]}`,
		"type after content":   blocks(`{"content":"` + c + `","type":8}`),
		"duplicate content":    blocks(`{"type":8,"content":"","content":"` + c + `"}`),
		"duplicate type":       blocks(`{"type":1,"content":"` + c + `","type":8}`),
		"after a non-object":   blocks(`null,3,"x",[],{"type":8,"content":"` + c + `"}`),
		"non-string content":   blocks(`{"type":8,"content":["` + c + `"]}`),
		"wrapped":              `{"message":{"id":"m","content_blocks":[{"type": 8,"content":"` + c + `"}]}}`,
		"wrapped, escaped key": `{"\u006dessage":{"id":"m","content_blocks":[{"type":8,"content":"` + c + `"}]}}`,
		"both levels":          `{"content_blocks":[{"type": 8,"content":"` + c + `"}],"message":{"content_blocks":[{"type" :8,"content":"` + c + `"}]}}`,
		"indented":             "{\n  \"message\": {\n    \"id\": \"m\",\n    \"content_blocks\": [\n      {\n        \"type\": 8,\n        \"content\": \"" + c + "\"\n      }\n    ]\n  }\n}",
	}
	for name, raw := range spellings {
		for _, mode := range []reliantv1.ToolBodyMode{unspecified, full, stub} {
			out := PreviewMessageUpdate([]byte(raw), mode)
			assert.NotContains(t, string(out), c, "%s, mode %v", name, mode)
			for _, content := range sealedContents(t, out) {
				assert.Equal(t, "", content, "%s, mode %v", name, mode)
			}
			assert.NotEmpty(t, sealedContents(t, out), "%s: the block is still sent, mode %v", name, mode)
		}
	}
}

// FuzzPreviewMessageUpdateNeverShipsRedactedThinking: whatever a stored
// update holds, no block a client could read as redacted_thinking leaves
// with content.
func FuzzPreviewMessageUpdateNeverShipsRedactedThinking(f *testing.F) {
	f.Add([]byte(`{"content_blocks":[{"type": 8,"content":"x"}]}`), uint8(0))
	f.Add([]byte(`{"message":{"content_blocks":[{"type":8.0,"content":"x"}]}}`), uint8(1))
	f.Add([]byte(`{"content\u005fblocks":[{"\u0074ype":"8","content":"x"}]}`), uint8(2))
	f.Add([]byte("{\"content_blocks\":[{\"type\"\t:\n8,\"content\":\"x\",\"content\":\"y\"}]}"), uint8(1))
	f.Add([]byte(`{"content_blocks":[{"type":8,"content":"0"}]}0`), uint8(0)) // trailing bytes after the object
	f.Fuzz(func(t *testing.T, raw []byte, m uint8) {
		mode := []reliantv1.ToolBodyMode{unspecified, full, stub}[int(m)%3]
		out := PreviewMessageUpdate(raw, mode)
		dec := json.NewDecoder(strings.NewReader(string(out)))
		dec.UseNumber()
		var payload map[string]any
		if dec.Decode(&payload) != nil {
			return // not an object: no client reads blocks from it
		}
		for _, blocks := range blockLists(payload) {
			for _, b := range blocks {
				block, ok := b.(map[string]any)
				if !ok || !isSealedBlockType(block["type"]) {
					continue
				}
				if content, present := block["content"]; present && content != "" {
					t.Fatalf("redacted_thinking content shipped: %s -> %s", raw, out)
				}
			}
		}
	})
}
