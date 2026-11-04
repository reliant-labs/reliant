// Copyright (c) 2025 Reliant Labs
package message

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/messageproto"
)

// GetMessage retrieves a single message by ID with its content blocks
func (s *Service) GetMessage(
	ctx context.Context,
	req *connect.Request[reliantv1.GetMessageRequest],
) (*connect.Response[reliantv1.GetMessageResponse], error) {
	messageID := req.Msg.MessageId
	if messageID == "" {
		return nil, svcerr.InvalidArgument("message_id is required")
	}

	// Get message from database
	msg, err := s.deps.Repo.GetMessage(ctx, messageID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get message"))
	}

	// Ownership is transitive through the chat: a message id alone is not an
	// authorization in the multi-tenant messages table. A foreign message is
	// NotFound, never PermissionDenied, so this cannot be used as a
	// cross-tenant existence oracle.
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	chat, err := s.deps.Repo.GetChatRow(ctx, msg.ChatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get message"))
	}
	if chat == nil || chat.UserID != userID {
		return nil, svcerr.NotFound("message")
	}

	// Get content blocks for this message
	blocks, err := s.deps.Repo.ListContentBlocks(ctx, msg.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load message content"))
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
		attachmentData, err := s.deps.Repo.GetAttachmentsByIDs(ctx, attachmentIDs)
		if err != nil {
			logging.WarnContext(ctx, "Failed to get attachments for message", "error", err, "messageID", msg.ID)
		} else {
			attachments = attachmentData
		}
	}

	// Build durable tool call status map so a reload/chat-switch sees real
	// status instead of the frontend having to infer it from workflow activity.
	toolCallStatusByCallID := make(map[string]*db.ToolCall)
	toolCalls, err := s.deps.Repo.ListToolCallsByMessageIDs(ctx, []string{msg.ID})
	if err != nil {
		logging.WarnContext(ctx, "Failed to list tool calls for message", "error", err, "messageID", msg.ID)
	} else {
		for _, call := range toolCalls {
			toolCallStatusByCallID[call.ID] = call
		}
	}

	// Convert to proto using shared helper from chat service.
	//
	// ViewingThreadID is deliberately unset: this fetches one message by id, and
	// the caller has not said which thread it is reading as. Statuses are
	// reported exactly as persisted; the branch-aware reinterpretation belongs
	// to ListMessages, which knows the thread being displayed.
	protoMessage := messageproto.FromDB(msg, blocks, attachments, &messageproto.Options{
		ToolCallStatusByCallID: toolCallStatusByCallID,
		SenderTitlesByThreadID: messageproto.SenderTitles(ctx, s.deps.Repo, []*db.Message{msg}),
	})

	return connect.NewResponse(&reliantv1.GetMessageResponse{
		Message: protoMessage,
	}), nil
}
