// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
)

// MessageService implements the MessageService RPC handlers
type MessageService struct {
	reliantv1connect.UnimplementedMessageServiceHandler
	database db.Repository
}

// NewMessageService creates a new MessageService
func NewMessageService(database db.Repository) *MessageService {
	return &MessageService{
		database: database,
	}
}

// GetMessage retrieves a single message by ID with its content blocks
func (s *MessageService) GetMessage(
	ctx context.Context,
	req *connect.Request[reliantv1.GetMessageRequest],
) (*connect.Response[reliantv1.GetMessageResponse], error) {
	messageID := req.Msg.MessageId
	if messageID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message_id is required"))
	}

	// Get message from database
	msg, err := s.database.GetMessage(ctx, messageID)
	if err != nil {
		logging.Error("Failed to get message", "error", err, "messageID", messageID)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("message not found"))
	}

	// Get content blocks for this message
	blocks, err := s.database.ListContentBlocks(ctx, msg.ID)
	if err != nil {
		logging.Error("Failed to list content blocks", "error", err, "messageID", msg.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load message content"))
	}

	// Collect attachment IDs from image and file_reference blocks and fetch attachments
	var attachments []*db.Attachment
	attachmentIDSet := make(map[string]bool)
	for _, block := range blocks {
		if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
			attachmentIDSet[*block.Content] = true
		}
	}
	if len(attachmentIDSet) > 0 {
		attachmentIDs := make([]string, 0, len(attachmentIDSet))
		for id := range attachmentIDSet {
			attachmentIDs = append(attachmentIDs, id)
		}
		attachmentData, err := s.database.GetAttachmentsByIDs(ctx, attachmentIDs)
		if err != nil {
			logging.Warn("Failed to get attachments for message", "error", err, "messageID", msg.ID)
		} else {
			attachments = attachmentData
		}
	}

	// Build tool results map for matching tool_call blocks with tool_result blocks
	toolResultsByCallID := make(map[string]*reliantv1.MatchedToolResult)
	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT && block.ToolCallID != nil {
			toolResultsByCallID[*block.ToolCallID] = &reliantv1.MatchedToolResult{
				ToolCallId: *block.ToolCallID,
				Type:       "tool_result",
				Content:    block.Content,
				IsError:    block.IsError,
			}
		}
	}

	// Convert to proto using shared helper from chat service
	protoMessage := messageToProto(msg, blocks, attachments, &MessageToProtoOptions{
		ToolResultsByCallID: toolResultsByCallID,
	})

	return connect.NewResponse(&reliantv1.GetMessageResponse{
		Message: protoMessage,
	}), nil
}
