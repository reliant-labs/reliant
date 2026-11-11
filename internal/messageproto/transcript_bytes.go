// Copyright (c) 2025 Reliant Labs

package messageproto

import (
	"encoding/json"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TranscriptBytes is what a transcript read sent, split by the kind of
// content block the bytes belong to. Every read path that delivers messages
// records one on its span (Attributes), so the share of a transcript that is
// tool bodies, and thinking, is measured on real traffic rather than
// estimated (dev-docs/design/chat-lazy-tool-bodies.md, "PR 0").
//
// It only sums lengths of strings the read already holds: nothing is copied
// or marshaled to measure it.
type TranscriptBytes struct {
	// Messages is the number of messages sent: proto Messages, or MESSAGE
	// chat updates on the JSON carriers.
	Messages int
	// Bytes is the wire size of those messages: proto.Size of each Message,
	// or the length of each MESSAGE update's data_json. It includes
	// everything a message carries besides its block bodies (ids,
	// timestamps, status), so it is at least the sum of the kinds below.
	Bytes int64

	// The bodies of the blocks sent, by kind. A block's body is its content
	// plus its input; each block counts under exactly one kind.
	Text       BlockBytes // TEXT
	ToolInput  BlockBytes // TOOL_CALL: the call's input
	ToolResult BlockBytes // TOOL_RESULT: the content as sent (a preview when truncated)
	Thinking   BlockBytes // THINKING and REDACTED_THINKING
	Other      BlockBytes // everything else: images, file references, documents, unknown kinds

	// ToolBodies is the mode the read was served in (ResolveToolBodyMode),
	// set by the caller; unset records none.
	ToolBodies reliantv1.ToolBodyMode
	// Elided is the blocks a TOOL_BODY_MODE_STUB read sent body_elided, and
	// the bytes of their stored bodies it left out: body_size less what was
	// sent instead (a call's skeleton, a result's stub; nothing for
	// thinking). The kinds above count what was sent, so they already
	// exclude these.
	Elided BlockBytes
}

// BlockBytes counts the blocks of one kind and the bytes of their bodies.
type BlockBytes struct {
	Blocks int
	Bytes  int64
}

func (b *BlockBytes) add(n int) {
	b.Blocks++
	b.Bytes += int64(n)
}

// Blocks is the number of blocks of every kind.
func (t *TranscriptBytes) Blocks() int {
	return t.Text.Blocks + t.ToolInput.Blocks + t.ToolResult.Blocks + t.Thinking.Blocks + t.Other.Blocks
}

// kind is the bucket a block of type bt counts under.
func (t *TranscriptBytes) kind(bt reliantv1.ContentBlockType) *BlockBytes {
	switch bt {
	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT:
		return &t.Text
	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL:
		return &t.ToolInput
	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT:
		return &t.ToolResult
	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING,
		reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING:
		return &t.Thinking
	default:
		return &t.Other
	}
}

// AddMessages counts messages as they are sent: after Assemble or FromDB,
// so a tool result counts as its preview, not as stored.
func (t *TranscriptBytes) AddMessages(messages ...*reliantv1.Message) {
	for _, m := range messages {
		if m == nil {
			continue
		}
		t.Messages++
		t.Bytes += int64(proto.Size(m))
		for _, b := range m.GetContentBlocks() {
			sent := len(b.GetContent()) + len(b.GetInput())
			t.kind(b.GetType()).add(sent)
			if b.GetBodyElided() {
				t.Elided.add(int(max(b.GetBodySize()-int64(sent), 0)))
			}
		}
	}
}

// updateBlock is the part of a live message update's content block that
// AddChatUpdate reads. The bodies stay raw: their encoded length is what the
// client receives, and leaving them undecoded skips unescaping and copying
// every string just to measure it.
type updateBlock struct {
	Type       json.RawMessage `json:"type"`
	Content    json.RawMessage `json:"content"`
	Input      json.RawMessage `json:"input"`
	BodyElided bool            `json:"body_elided"`
	BodySize   int64           `json:"body_size"`
}

type updateMessage struct {
	ContentBlocks []updateBlock `json:"content_blocks"`
}

// updatePayload is a MESSAGE update's data: the message, either wrapped in
// {"message": ...} (what the stream sends) or bare (how some rows are
// stored).
type updatePayload struct {
	Message *updateMessage `json:"message"`
	updateMessage
}

// AddChatUpdate counts one chat update as it is sent on a JSON carrier (the
// stream's replay and live frames, GetChatUpdates): data is its data_json,
// after PreviewMessageUpdate. Only MESSAGE updates carry transcript; the rest
// are ignored. A block's bytes here are its JSON-encoded content and input,
// which is what goes on the wire, so escaping makes them slightly larger
// than the same block on a proto read.
//
// A payload that does not parse still counts as a message of len(data)
// bytes, with no blocks.
func (t *TranscriptBytes) AddChatUpdate(updateType reliantv1.ChatUpdateType, data string) {
	if updateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE {
		return
	}
	t.Messages++
	t.Bytes += int64(len(data))

	var payload updatePayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return
	}
	blocks := payload.ContentBlocks
	if payload.Message != nil {
		blocks = payload.Message.ContentBlocks
	}
	for _, b := range blocks {
		sent := jsonStringLen(b.Content) + jsonStringLen(b.Input)
		t.kind(updateBlockType(b.Type)).add(sent)
		if b.BodyElided {
			t.Elided.add(int(max(b.BodySize-int64(sent), 0)))
		}
	}
}

// updateBlockType reads a block type spelled as its number (how
// db.ContentBlockPayloadsWithToolCalls marshals it) or its enum name.
func updateBlockType(raw json.RawMessage) reliantv1.ContentBlockType {
	if len(raw) > 0 && raw[0] == '"' {
		var name string
		if json.Unmarshal(raw, &name) == nil {
			return reliantv1.ContentBlockType(reliantv1.ContentBlockType_value[name])
		}
		return reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_UNSPECIFIED
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil {
		return reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_UNSPECIFIED
	}
	return reliantv1.ContentBlockType(n)
}

// jsonStringLen is the encoded length of a JSON string value without its
// quotes; 0 for null, absent, or anything that is not a string.
func jsonStringLen(raw json.RawMessage) int {
	if len(raw) < 2 || raw[0] != '"' {
		return 0
	}
	return len(raw) - 2
}

// Attributes renders t as span attributes under prefix: prefix.messages,
// prefix.bytes, prefix.blocks, prefix.<kind>_bytes / prefix.<kind>_blocks
// for kind in text, tool_input, tool_result, thinking and other, and
// prefix.blocks_elided / prefix.bytes_elided; plus prefix.tool_bodies when
// ToolBodies is set.
func (t *TranscriptBytes) Attributes(prefix string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.Int(prefix+".messages", t.Messages),
		attribute.Int64(prefix+".bytes", t.Bytes),
		attribute.Int(prefix+".blocks", t.Blocks()),
	}
	for _, k := range []struct {
		name string
		b    BlockBytes
	}{
		{"text", t.Text},
		{"tool_input", t.ToolInput},
		{"tool_result", t.ToolResult},
		{"thinking", t.Thinking},
		{"other", t.Other},
	} {
		attrs = append(attrs,
			attribute.Int64(prefix+"."+k.name+"_bytes", k.b.Bytes),
			attribute.Int(prefix+"."+k.name+"_blocks", k.b.Blocks))
	}
	attrs = append(attrs,
		attribute.Int(prefix+".blocks_elided", t.Elided.Blocks),
		attribute.Int64(prefix+".bytes_elided", t.Elided.Bytes))
	if t.ToolBodies != reliantv1.ToolBodyMode_TOOL_BODY_MODE_UNSPECIFIED {
		attrs = append(attrs, attribute.String(prefix+".tool_bodies", t.ToolBodies.String()))
	}
	return attrs
}
