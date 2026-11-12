// Copyright (c) 2025 Reliant Labs

package messageproto

import (
	"bytes"
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// A VIEWER reads a chat its owner shared with their org (control-plane
// docs/design/org-sharing.md §4, decision D2): the transcript and every tool
// call's input, but no tool's result. A result embeds what the tool read off
// the owner's machine (file contents, command output, secrets in either), so
// it is left out entirely, not previewed, and GetContentBlockBodies and
// GetToolResult refuse a viewer, so it cannot be fetched either.
//
// The results the client parses to build its own state (plans, tasks,
// questions; structuredResultTools) are the conversation's, not the
// machine's, and stay whole.

// ElideForViewer leaves every tool result out of msgs, in place: content
// removed, body_elided set and body_size the stored length. Applied after
// the transcript is assembled (in whatever tool body mode), on every
// transcript read a viewer makes.
func ElideForViewer(msgs []*reliantv1.Message) {
	names := map[string]string{}
	for _, m := range msgs {
		for _, b := range m.GetContentBlocks() {
			if b.GetType() == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && b.GetToolCallId() != "" {
				names[b.GetToolCallId()] = b.GetToolName()
			}
		}
	}
	for _, m := range msgs {
		for _, b := range m.GetContentBlocks() {
			elideBlockForViewer(b, names)
		}
	}
}

// ElideBlocksForViewer is ElideForViewer over loose blocks (a block-body read).
func ElideBlocksForViewer(blocks []*reliantv1.ContentBlock) {
	names := map[string]string{}
	for _, b := range blocks {
		if b.GetType() == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && b.GetToolCallId() != "" {
			names[b.GetToolCallId()] = b.GetToolName()
		}
	}
	for _, b := range blocks {
		elideBlockForViewer(b, names)
	}
}

func elideBlockForViewer(b *reliantv1.ContentBlock, names map[string]string) {
	if b == nil || b.GetType() != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT {
		return
	}
	name := b.GetToolName()
	if name == "" {
		name = names[b.GetToolCallId()]
	}
	if isStructuredResultTool(name) || b.Content == nil {
		return
	}
	size := int64(len(b.GetContent()))
	if b.OriginalSize != nil {
		size = b.GetOriginalSize()
	}
	b.Content = nil
	b.Truncated = nil
	b.OriginalSize = nil
	b.BodyElided = ptr.Of(true)
	b.BodySize = ptr.Of(size)
}

// ViewerMessageUpdate is ElideForViewer for a message update's JSON payload
// (a CHAT_UPDATE_TYPE_MESSAGE row, possibly wrapped in {"message": ...}): the
// form replays and live stream frames carry. A tool result's content is
// removed and body_elided/body_size set. It fails closed: a payload that
// carries content blocks but cannot be decoded is replaced by an empty
// object, never sent as it is.
func ViewerMessageUpdate(raw []byte) []byte {
	if !bytes.Contains(raw, []byte(`"content_blocks"`)) {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return []byte(`{}`)
	}
	msg := payload
	if wrapped, ok := payload["message"].(map[string]any); ok {
		msg = wrapped
	}
	blocks, _ := msg["content_blocks"].([]any)
	names := map[string]string{}
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok || !isBlockType(block["type"], reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL) {
			continue
		}
		id, _ := block["tool_call_id"].(string)
		name, _ := block["tool_name"].(string)
		if id != "" {
			names[id] = name
		}
	}
	changed := false
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok || !isToolResultBlockType(block["type"]) {
			continue
		}
		content, ok := block["content"].(string)
		if !ok {
			continue
		}
		name, _ := block["tool_name"].(string)
		if name == "" {
			id, _ := block["tool_call_id"].(string)
			name = names[id]
		}
		if isStructuredResultTool(name) {
			continue
		}
		size := len(content)
		if n, ok := block["original_size"].(json.Number); ok {
			if v, err := n.Int64(); err == nil {
				size = int(v)
			}
		}
		delete(block, "content")
		delete(block, "truncated")
		delete(block, "original_size")
		block["body_elided"] = true
		block["body_size"] = size
		changed = true
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{}`)
	}
	return out
}
