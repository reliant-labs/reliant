// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// MessageToProtoOptions configures optional behavior for message conversion.
type MessageToProtoOptions struct {
	// ToolResultsByCallID maps tool_call IDs to their matched results.
	// When non-nil, tool_call content blocks will have their MatchedResult populated.
	ToolResultsByCallID map[string]*reliantv1.MatchedToolResult

	// ToolCallStatusByCallID maps tool_call IDs to their durable status row.
	// When non-nil, tool_call content blocks have tool_call_status/started_at/
	// completed_at populated from it, giving the read path a real status
	// instead of leaving the frontend to infer one from workflow activity.
	ToolCallStatusByCallID map[string]*db.ToolCall

	// ViewingThreadID is the thread the messages are being read for. A branch
	// displays history it does not own, so a call still running in the thread
	// that started it is not running here: its result will be written to that
	// thread, and nothing will ever resolve it in this one. Set this to have
	// such calls reported as cancelled rather than as this thread's own
	// in-flight work. Empty leaves every status as persisted.
	ViewingThreadID string

	// SequenceNumber, if non-zero, is set on the resulting proto message.
	// Used by streaming snapshots to tag messages with the latest sequence.
	SequenceNumber int64
}

// toolCallStatusToProto converts the durable core.ToolCallStatus to the
// mirrored proto enum. Values are defined to match numerically, but the
// explicit switch keeps a mismatch a compile-time-visible bug instead of a
// silent int32 cast.
func toolCallStatusToProto(s core.ToolCallStatus) reliantv1.ToolCallStatus {
	switch s {
	case core.ToolCallStatusPending:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_PENDING
	case core.ToolCallStatusExecuting:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_EXECUTING
	case core.ToolCallStatusCompleted:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_COMPLETED
	case core.ToolCallStatusFailed:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_FAILED
	case core.ToolCallStatusCancelled:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_CANCELLED
	case core.ToolCallStatusBackgrounded:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_BACKGROUNDED
	default:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_UNSPECIFIED
	}
}

// contentBlockToProto converts a db.MessageContentBlock to proto ContentBlock.
// inheritedInFlightCall reports whether call is running in a different thread
// than the one being viewed. Such a call is unresolvable from here: the thread
// that owns it will receive its result, and this thread only inherited the
// request. A call with no recorded thread cannot be attributed either way, so it
// is left alone.
func inheritedInFlightCall(call *db.ToolCall, viewingThreadID string) bool {
	if viewingThreadID == "" || call.ThreadID == nil || call.Status.IsTerminal() {
		return false
	}
	return *call.ThreadID != viewingThreadID
}

func contentBlockToProto(b *db.MessageContentBlock, toolResultsByCallID map[string]*reliantv1.MatchedToolResult, toolCallStatusByCallID map[string]*db.ToolCall, viewingThreadID string) *reliantv1.ContentBlock {
	proto := &reliantv1.ContentBlock{
		Id:    b.ID,
		Index: int32(b.Position),
		Type:  b.BlockType,
	}
	if b.Content != nil {
		proto.Content = b.Content
	}
	if b.ToolName != nil {
		proto.ToolName = b.ToolName
	}
	if b.ToolCallID != nil {
		proto.ToolCallId = b.ToolCallID
		// For tool_call blocks, attach matched result and durable status if available
		if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL {
			if result, found := toolResultsByCallID[*b.ToolCallID]; found {
				proto.MatchedResult = result
			}
			if call, found := toolCallStatusByCallID[*b.ToolCallID]; found {
				status := toolCallStatusToProto(call.Status)
				if inheritedInFlightCall(call, viewingThreadID) {
					status = reliantv1.ToolCallStatus_TOOL_CALL_STATUS_CANCELLED
				}
				proto.ToolCallStatus = &status
				if call.StartedAt != nil {
					proto.StartedAt = ptr.Of(call.StartedAt.Format(time.RFC3339Nano))
				}
				if call.CompletedAt != nil {
					proto.CompletedAt = ptr.Of(call.CompletedAt.Format(time.RFC3339Nano))
				}
				// The workflow a spawn started. A spawned sub-agent's thread id
				// equals its workflow id, so this tells the renderer directly
				// which thread the call owns instead of making it search the
				// workflow tree by node id.
				if call.ChildWorkflowID != nil && *call.ChildWorkflowID != "" {
					proto.ChildWorkflowId = call.ChildWorkflowID
				}
			}
		}
	}
	if b.ToolInput != nil {
		proto.Input = b.ToolInput
	}
	if b.IsError != nil {
		proto.IsError = b.IsError
	}
	return proto
}

// messageToProto converts a db.Message to proto Message.
// blocks and attachments are required; opts is optional (pass nil for defaults).
func messageToProto(m *db.Message, blocks []*db.MessageContentBlock, attachments []*db.Attachment, opts *MessageToProtoOptions) *reliantv1.Message {
	if opts == nil {
		opts = &MessageToProtoOptions{}
	}

	// Compute streaming state from blocks
	blockValues := make([]db.MessageContentBlock, len(blocks))
	for i, block := range blocks {
		if block != nil {
			blockValues[i] = *block
		}
	}
	streamingState := db.ComputeStreamingState(blockValues)

	proto := &reliantv1.Message{
		Id:             m.ID,
		ChatId:         m.ChatID,
		Seq:            m.Seq,
		Thread:         m.ThreadID,
		Role:           m.Role,
		StreamingState: streamingStateFromComputedString(streamingState.State),
		CreatedAt:      m.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:      m.UpdatedAt.Format(time.RFC3339Nano),
	}
	if m.Model != nil {
		proto.Model = m.Model
	}
	if m.Agent != nil {
		proto.Agent = m.Agent
	}
	if m.TokenCount != nil {
		proto.InputTokens = ptr.Of(int32(*m.TokenCount))
	}
	if m.WorkflowID != nil {
		proto.WorkflowId = m.WorkflowID
	}
	if m.NodeID != nil {
		proto.NodeId = m.NodeID
	}
	if m.DisplayStyle != nil {
		proto.DisplayStyle = m.DisplayStyle
	}
	if opts.SequenceNumber != 0 {
		proto.SequenceNumber = opts.SequenceNumber
	}

	// Convert content blocks
	protoBlocks := make([]*reliantv1.ContentBlock, len(blocks))
	for i, b := range blocks {
		protoBlocks[i] = contentBlockToProto(b, opts.ToolResultsByCallID, opts.ToolCallStatusByCallID, opts.ViewingThreadID)
	}
	proto.ContentBlocks = protoBlocks

	// Convert attachments
	protoAttachments := make([]*reliantv1.Attachment, len(attachments))
	for i, a := range attachments {
		protoAttachments[i] = &reliantv1.Attachment{
			Id:       a.ID,
			Filename: a.Filename,
			Size:     a.Size,
			MimeType: a.MimeType,
			Url:      fmt.Sprintf("/api/attachments/%s", a.ID),
		}
	}
	proto.Attachments = protoAttachments

	return proto
}

// assembleMessagesForDisplay converts a page of messages to proto WITH the
// enrichment every display path needs: matched tool results, durable tool-call
// status, and the child_workflow_id that tells a spawn which thread it owns.
//
// It exists because that enrichment used to be open-coded at each call site,
// and the streaming snapshot silently didn't do it — it passed only
// SequenceNumber. A live spawn's tool-call block therefore arrived with no
// child_workflow_id, so its preview had no thread to read and rendered
// "Starting…" for the entire run, while the very same call reloaded through
// ListMessages showed the transcript. Two read paths for one screen disagreed,
// and the one that was wrong was the one that mattered while work was
// happening.
//
// A caller cannot opt out of the enrichment: that's the point. If a read path
// wants messages for display, it gets them assembled the same way as every
// other read path.
//
// blocksByMessageID and attachments must already be fetched; this does the
// per-message grouping and the tool-call lookups.
func assembleMessagesForDisplay(
	ctx context.Context,
	database db.Repository,
	messages []*db.Message,
	messageIDs []string,
	blocksByMessageID map[string][]*db.MessageContentBlock,
	attachmentMap map[string]*db.Attachment,
	viewingThreadID string,
	sequenceNumber int64,
) []*reliantv1.Message {
	// Matched tool results, keyed by call id. Results arrive as their own
	// TOOL-role messages, so they must be collected across the whole page
	// before any single message is converted.
	toolResultsByCallID := make(map[string]*reliantv1.MatchedToolResult)
	for _, msg := range messages {
		if msg.Role != reliantv1.MessageRole_MESSAGE_ROLE_TOOL {
			continue
		}
		for _, block := range blocksByMessageID[msg.ID] {
			if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT && block.ToolCallID != nil {
				result := &reliantv1.MatchedToolResult{
					ToolCallId: *block.ToolCallID,
					Type:       "tool_result",
				}
				if block.Content != nil {
					result.Content = block.Content
				}
				if block.IsError != nil {
					result.IsError = block.IsError
				}
				toolResultsByCallID[*block.ToolCallID] = result
			}
		}
	}

	// Durable tool-call rows: real status instead of the frontend inferring
	// one from workflow activity, plus child_workflow_id for spawns.
	//
	// Read BOTH ways and merge. The by-message query depends on
	// tool_calls.message_id, a link the writer must remember to set — and the
	// two writers that run during a live tool call (the execute_tools activity
	// and EmitToolCallStatus on the spawn path) have no message in hand, so
	// their rows carry NULL and `WHERE message_id IN (...)` cannot see them at
	// all. That is why a running spawn's block arrived with no
	// child_workflow_id and its preview stayed on "Starting…" until a reload.
	//
	// The blocks always carry tool_call_id, so the by-id read finds those rows
	// regardless. The by-message read is kept because it also returns calls
	// whose block is not in this page.
	toolCallStatusByCallID := make(map[string]*db.ToolCall)
	toolCalls, err := database.ListToolCallsByMessageIDs(ctx, messageIDs)
	if err != nil {
		logging.Warn("Failed to list tool calls for messages", "error", err, "messageCount", len(messageIDs))
	} else {
		for _, call := range toolCalls {
			toolCallStatusByCallID[call.ID] = call
		}
	}

	blockToolCallIDs := make([]string, 0)
	for _, blocks := range blocksByMessageID {
		for _, b := range blocks {
			if b.ToolCallID != nil && *b.ToolCallID != "" {
				if _, found := toolCallStatusByCallID[*b.ToolCallID]; !found {
					blockToolCallIDs = append(blockToolCallIDs, *b.ToolCallID)
				}
			}
		}
	}
	if len(blockToolCallIDs) > 0 {
		byID, err := database.ListToolCallsByIDs(ctx, blockToolCallIDs)
		if err != nil {
			logging.Warn("Failed to list tool calls by id", "error", err, "count", len(blockToolCallIDs))
		} else {
			for _, call := range byID {
				toolCallStatusByCallID[call.ID] = call
			}
		}
	}

	protoMessages := make([]*reliantv1.Message, 0, len(messages))
	for _, msg := range messages {
		blocks := blocksByMessageID[msg.ID]

		// Hidden messages (e.g. compaction summaries) still go to the LLM but
		// are not part of the transcript.
		if msg.DisplayStyle != nil && *msg.DisplayStyle == reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN {
			continue
		}

		var msgAttachments []*db.Attachment
		for _, block := range blocks {
			if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
				if att, found := attachmentMap[*block.Content]; found {
					msgAttachments = append(msgAttachments, att)
				}
			}
		}

		protoMessages = append(protoMessages, messageToProto(msg, blocks, msgAttachments, &MessageToProtoOptions{
			ToolResultsByCallID:    toolResultsByCallID,
			ToolCallStatusByCallID: toolCallStatusByCallID,
			ViewingThreadID:        viewingThreadID,
			SequenceNumber:         sequenceNumber,
		}))
	}
	return protoMessages
}

// streamingStateFromComputedString converts the string returned by db.ComputeStreamingState
// ("streaming", "complete", "failed") to the proto enum.
func streamingStateFromComputedString(s string) reliantv1.StreamingState {
	switch s {
	case "streaming":
		return reliantv1.StreamingState_STREAMING_STATE_STREAMING
	case "complete":
		return reliantv1.StreamingState_STREAMING_STATE_COMPLETE
	case "failed":
		return reliantv1.StreamingState_STREAMING_STATE_FAILED
	default:
		return reliantv1.StreamingState_STREAMING_STATE_UNSPECIFIED
	}
}
