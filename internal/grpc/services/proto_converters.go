// Copyright (c) 2025 Reliant Labs
package services

import (
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// MessageToProtoOptions configures optional behavior for message conversion.
type MessageToProtoOptions struct {
	// ToolResultsByCallID maps tool_call IDs to their matched results.
	// When non-nil, tool_call content blocks will have their MatchedResult populated.
	ToolResultsByCallID map[string]*reliantv1.MatchedToolResult

	// SequenceNumber, if non-zero, is set on the resulting proto message.
	// Used by streaming snapshots to tag messages with the latest sequence.
	SequenceNumber int64
}

// contentBlockToProto converts a db.MessageContentBlock to proto ContentBlock.
func contentBlockToProto(b *db.MessageContentBlock, toolResultsByCallID map[string]*reliantv1.MatchedToolResult) *reliantv1.ContentBlock {
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
		// For tool_call blocks, attach matched result if available
		if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL {
			if result, found := toolResultsByCallID[*b.ToolCallID]; found {
				proto.MatchedResult = result
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
		Ordinal:        m.Ordinal,
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
	if m.DisplayStyle != nil {
		proto.DisplayStyle = m.DisplayStyle
	}
	if opts.SequenceNumber != 0 {
		proto.SequenceNumber = opts.SequenceNumber
	}

	// Convert content blocks
	protoBlocks := make([]*reliantv1.ContentBlock, len(blocks))
	for i, b := range blocks {
		protoBlocks[i] = contentBlockToProto(b, opts.ToolResultsByCallID)
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
