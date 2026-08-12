package db

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ContentBlockPayloads serializes content blocks for the `content_blocks`
// field of a live message update — the wire shape the client parses through
// MessageSchema.
//
// This exists because four separate call sites each open-coded the same
// field-by-field loop (`id`/`type`/`index`, then `content`, `tool_name`,
// `input`, `tool_call_id`, `is_error`), and a block gained a field that only
// some of them learned about. tool_calls.child_workflow_id is what names the
// thread a spawn owns; the read paths carried it, the live-update writers did
// not. So a spawn's preview had nothing to render for the entire time the
// spawn was running, and then filled in correctly the moment the chat was
// reloaded through a different path.
//
// A block's wire shape is one fact. Duplicating it four ways guarantees the
// copies drift, and the drift shows up as a UI that is right after a reload
// and wrong while you watch it — the hardest kind to trust a fix for.
//
// Tool-call blocks are enriched from the durable tool_calls rows, so status
// and child_workflow_id travel with the block the same way they do on the
// read paths (see assembleMessagesForDisplay). A nil-safe lookup is used
// rather than requiring callers to pre-fetch: the rows are keyed by the ids
// already present in the blocks.
func (r *Repo) ContentBlockPayloads(ctx context.Context, blocks []*MessageContentBlock) []map[string]interface{} {
	toolCallIDs := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.ToolCallID != nil && *block.ToolCallID != "" {
			toolCallIDs = append(toolCallIDs, *block.ToolCallID)
		}
	}

	toolCallsByID := make(map[string]*ToolCall, len(toolCallIDs))
	if len(toolCallIDs) > 0 {
		// Keyed by the block's own tool_call_id. The by-message read depends
		// on tool_calls.message_id, which the live writers could not populate;
		// this one asks for exactly the calls these blocks name.
		if calls, err := r.ListToolCallsByIDs(ctx, toolCallIDs); err != nil {
			// Non-fatal: the block still renders, it just won't carry durable
			// status. Logged rather than swallowed so a systematic failure is
			// visible instead of showing up as spawns that never preview.
			logging.Warn("Failed to load tool calls for content block payload", "error", err)
		} else {
			for _, call := range calls {
				toolCallsByID[call.ID] = call
			}
		}
	}

	return ContentBlockPayloadsWithToolCalls(blocks, toolCallsByID)
}

// ContentBlockPayloadsWithToolCalls is the pure serialization half, for
// callers that already hold the tool-call rows (and for tests, which need no
// database to check the wire shape).
func ContentBlockPayloadsWithToolCalls(
	blocks []*MessageContentBlock,
	toolCallsByID map[string]*ToolCall,
) []map[string]interface{} {
	payloads := make([]map[string]interface{}, 0, len(blocks))
	for _, block := range blocks {
		blockData := map[string]interface{}{
			"id":    block.ID,
			"type":  block.BlockType,
			"index": block.Position,
		}

		if block.Content != nil {
			blockData["content"] = *block.Content
		}
		if block.ToolName != nil {
			blockData["tool_name"] = *block.ToolName
		}
		if block.ToolInput != nil {
			blockData["input"] = *block.ToolInput
		}
		if block.IsError != nil {
			blockData["is_error"] = *block.IsError
		}
		if block.ToolCallID != nil {
			blockData["tool_call_id"] = *block.ToolCallID

			if call, found := toolCallsByID[*block.ToolCallID]; found {
				blockData["tool_call_status"] = call.Status
				if call.StartedAt != nil {
					blockData["started_at"] = call.StartedAt.Format(timestampLayout)
				}
				if call.CompletedAt != nil {
					blockData["completed_at"] = call.CompletedAt.Format(timestampLayout)
				}
				// Only when a child really exists. An empty string would make
				// "this spawn owns a thread" indistinguishable from "it does
				// not", which is the ambiguity the consumer must not have.
				if call.ChildWorkflowID != nil && *call.ChildWorkflowID != "" {
					blockData["child_workflow_id"] = *call.ChildWorkflowID
				}
			}
		}

		payloads = append(payloads, blockData)
	}
	return payloads
}

// AttachmentIDsFromBlocks returns the attachment ids referenced by image and
// file_reference blocks, in block order.
func AttachmentIDsFromBlocks(blocks []*MessageContentBlock) []string {
	ids := []string{}
	for _, block := range blocks {
		if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE ||
			block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) &&
			block.Content != nil {
			ids = append(ids, *block.Content)
		}
	}
	return ids
}

// timestampLayout matches the format the read paths use for these fields, so
// a block looks the same whether it arrived live or on reload.
const timestampLayout = "2006-01-02T15:04:05.999999999Z07:00"
