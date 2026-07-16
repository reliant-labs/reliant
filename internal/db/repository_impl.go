// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Ensure *Repo implements Repository interface at compile time
var _ Repository = (*Repo)(nil)

// ==================== Chats ====================

func (r *Repo) CreateChat(ctx context.Context, chat *Chat) error {
	if chat == nil {
		return fmt.Errorf("chat cannot be nil")
	}
	return r.chats.CreateChat(ctx, chat)
}

func (r *Repo) GetChat(ctx context.Context, id string) (*Chat, error) {
	if id == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return r.chats.GetChat(ctx, id)
}

func (r *Repo) GetChatWithUserCheck(ctx context.Context, id string, userID string) (*Chat, error) {
	if id == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	return r.chats.GetChatWithUserCheck(ctx, id, userID)
}

func (r *Repo) ListChats(ctx context.Context, filters ChatFilters) ([]*Chat, error) {
	return r.chats.ListChats(ctx, filters)
}

func (r *Repo) SearchChats(ctx context.Context, filters ChatSearchFilters) ([]*Chat, error) {
	return r.chats.SearchChats(ctx, filters)
}

func (r *Repo) UpdateChat(ctx context.Context, chat *Chat) error {
	if chat == nil {
		return fmt.Errorf("chat cannot be nil")
	}
	if chat.ID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return r.chats.UpdateChat(ctx, chat)
}

func (r *Repo) DeleteChat(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return r.chats.DeleteChat(ctx, id)
}

func (r *Repo) ListArchivedChats(ctx context.Context, userID string) ([]*ArchivedChatInfo, error) {
	return r.chats.ListArchivedChats(ctx, userID)
}

// ==================== Messages ====================

// CreateMessage creates a message record in the database.
//
// WARNING: Prefer SaveMessageToThread for most use cases outside workflow activities.
// This low-level function requires you to manually set Ordinal and ContextSequence,
// which is error-prone. SaveMessageToThread handles these automatically and correctly
// inherits context_sequence for forked workflows.
//
// Only use CreateMessage directly when:
// - You're inside a workflow activity that manages its own ordinal/context_sequence
// - You need very specific control over message placement (rare)
func (r *Repo) CreateMessage(ctx context.Context, msg *Message) error {
	if msg == nil {
		return fmt.Errorf("message cannot be nil")
	}
	if msg.ID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}
	if msg.ChatID == "" {
		return fmt.Errorf("message ChatID cannot be empty")
	}
	return r.messages.CreateMessage(ctx, msg)
}

// CreateMessageIfNotExists creates a message record if it doesn't already exist (INSERT OR IGNORE).
//
// WARNING: Same caveats as CreateMessage - prefer SaveMessageToThread for most cases.
// This is primarily for idempotent workflow activity implementations.
func (r *Repo) CreateMessageIfNotExists(ctx context.Context, msg *Message) error {
	if msg == nil {
		return fmt.Errorf("message cannot be nil")
	}
	if msg.ID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}
	if msg.ChatID == "" {
		return fmt.Errorf("message ChatID cannot be empty")
	}
	return r.messages.CreateMessageIfNotExists(ctx, msg)
}

// SaveMessageToThread atomically creates a message with text and optional image content blocks.
// This is the PREFERRED method for saving messages outside of workflow activities.
// It automatically handles:
//   - Getting the next ordinal for the thread
//   - Determining the correct context_sequence (including fork inheritance)
//   - Creating content blocks atomically
//   - Creating chat_update for frontend streaming
//
// Use this instead of CreateMessage directly to avoid context_sequence bugs.
// attachmentIDs are optional - if provided, image blocks are created for each attachment.
func int32DisplayStyleToProto(value *int32) *reliantv1.DisplayStyle {
	if value == nil {
		return nil
	}
	displayStyle := reliantv1.DisplayStyle(*value)
	return &displayStyle
}

func (r *Repo) SaveMessageToThread(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32) (*Message, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chatID cannot be empty")
	}
	if thread == "" {
		return nil, fmt.Errorf("thread cannot be empty")
	}
	if role == 0 {
		return nil, fmt.Errorf("role cannot be unspecified")
	}

	var savedMsg *Message
	err := r.RunTx(ctx, func(txCtx context.Context) error {
		// Get next ordinal for the thread
		ordinal, err := r.GetNextOrdinal(txCtx, thread)
		if err != nil {
			return fmt.Errorf("failed to get next ordinal: %w", err)
		}

		// Get the current context_sequence for the thread
		// This ensures user messages are saved at the same context_sequence as other messages
		// (important after compaction when context_sequence > 0)
		contextSequence, err := r.GetMaxContextSequenceInThread(txCtx, thread)
		if err != nil {
			return fmt.Errorf("failed to get context sequence: %w", err)
		}

		// Get or create context window for this thread at this sequence
		contextWindowID := fmt.Sprintf("%s:%s:%d", chatID, thread, contextSequence)
		cw, err := r.GetContextWindow(txCtx, contextWindowID)
		if err != nil || cw == nil {
			// Create new context window
			now := time.Now().UTC()
			newCW := &ContextWindow{
				ID:        contextWindowID,
				ThreadID:  thread,
				Sequence:  contextSequence,
				CreatedAt: now,
			}
			if _, err := r.CreateContextWindow(txCtx, newCW); err != nil {
				return fmt.Errorf("failed to create context window: %w", err)
			}
		}

		// Create the message
		now := time.Now().UTC()
		msgID := uuid.New().String()
		msg := &Message{
			ID:              msgID,
			ChatID:          chatID,
			Ordinal:         ordinal,
			ThreadID:        thread,
			ContextWindowID: contextWindowID,
			Role:            reliantv1.MessageRole(role),
			DisplayStyle:    int32DisplayStyleToProto(displayStyle),
			WorkflowID:      workflowID,
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		if err := r.CreateMessage(txCtx, msg); err != nil {
			return fmt.Errorf("failed to create message: %w", err)
		}

		position := 0

		// Create text content block if there's content
		if content != "" {
			block := &MessageContentBlock{
				ID:        uuid.New().String(),
				MessageID: msgID,
				Position:  position,
				BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
				Content:   &content,
			}

			if err := r.CreateContentBlock(txCtx, block); err != nil {
				return fmt.Errorf("failed to create text content block: %w", err)
			}
			position++
		}

		// Create attachment content blocks for each attachment based on stored attachment type
		type attachmentBlockDescriptor struct {
			id        string
			blockType reliantv1.ContentBlockType
		}
		attachmentBlocks := make([]attachmentBlockDescriptor, 0, len(attachmentIDs))
		for _, attachmentID := range attachmentIDs {
			att, err := r.GetAttachment(txCtx, attachmentID)
			if err != nil {
				return fmt.Errorf("failed to get attachment metadata for %s: %w", attachmentID, err)
			}

			// Duplicate of the switch in threads/save_message.go
			// (createContentBlocks) — the two must accept the same set of
			// attachment types or first-message saves diverge from
			// follow-up saves.
			var blockType reliantv1.ContentBlockType
			switch att.AttachmentType {
			case "image":
				blockType = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE
			case "file_reference":
				blockType = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE
			case "document":
				blockType = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_DOCUMENT
			default:
				return fmt.Errorf("invalid attachment type %q for attachment %s", att.AttachmentType, attachmentID)
			}

			attID := attachmentID // Capture for pointer
			block := &MessageContentBlock{
				ID:        uuid.New().String(),
				MessageID: msgID,
				Position:  position,
				BlockType: blockType,
				Content:   &attID,
			}

			if err := r.CreateContentBlock(txCtx, block); err != nil {
				return fmt.Errorf("failed to create attachment content block: %w", err)
			}
			attachmentBlocks = append(attachmentBlocks, attachmentBlockDescriptor{id: attID, blockType: blockType})
			position++
		}

		// Create chat_update for streaming to frontend
		// Build content blocks array
		contentBlocks := []map[string]interface{}{}
		blockIndex := 0
		if content != "" {
			contentBlocks = append(contentBlocks, map[string]interface{}{
				"id":      uuid.New().String(),
				"type":    reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
				"index":   blockIndex,
				"content": content,
			})
			blockIndex++
		}
		attachmentBlockTypeByID := make(map[string]reliantv1.ContentBlockType, len(attachmentBlocks))
		attachmentIDsForMetadata := make([]string, 0, len(attachmentBlocks))
		for _, block := range attachmentBlocks {
			contentBlocks = append(contentBlocks, map[string]interface{}{
				"id":      uuid.New().String(),
				"type":    block.blockType,
				"index":   blockIndex,
				"content": block.id,
			})
			attachmentIDsForMetadata = append(attachmentIDsForMetadata, block.id)
			attachmentBlockTypeByID[block.id] = block.blockType
			blockIndex++
		}

		// Fetch attachment metadata for attachment blocks
		attachments := []map[string]interface{}{}
		if len(attachmentIDsForMetadata) > 0 {
			attachmentsData, err := r.GetAttachmentsByIDs(txCtx, attachmentIDsForMetadata)
			if err != nil {
				return fmt.Errorf("failed to get attachment metadata: %w", err)
			}
			for _, att := range attachmentsData {
				attachments = append(attachments, map[string]interface{}{
					"id":        att.ID,
					"filename":  att.Filename,
					"size":      att.Size,
					"mime_type": att.MimeType,
					"url":       fmt.Sprintf("/api/attachments/%s", att.ID),
				})
			}
		}

		// Build update data matching the streaming service expectations
		updateData := map[string]interface{}{
			"update_type":      "message",
			"id":               msgID,
			"role":             role,
			"ordinal":          ordinal,
			"thread":           thread,
			"context_sequence": contextSequence,
			"created_at":       now.Format("2006-01-02T15:04:05.999999999Z07:00"),
			"updated_at":       now.Format("2006-01-02T15:04:05.999999999Z07:00"),
			"content_blocks":   contentBlocks,
			"attachments":      attachments,
		}

		updateDataJSON, err := json.Marshal(updateData)
		if err != nil {
			return fmt.Errorf("failed to marshal chat_update data: %w", err)
		}

		if err := r.CreateChatUpdate(txCtx, chatID, UpdateTypeMessage, msgID, string(updateDataJSON)); err != nil {
			return fmt.Errorf("failed to create chat_update: %w", err)
		}

		savedMsg = msg
		return nil
	})

	if err != nil {
		return nil, err
	}
	return savedMsg, nil

}

func (r *Repo) GetMessage(ctx context.Context, id string) (*Message, error) {
	if id == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}
	return r.messages.GetMessage(ctx, id)
}

func (r *Repo) GetNextOrdinal(ctx context.Context, threadID string) (int64, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	// Get the next ordinal from messages in this thread (all context windows)
	// NOTE: For workflow forks, each workflow has its own ordinal space.
	// Context inheritance is handled separately at LLM context resolution time.
	return r.messages.GetNextOrdinal(ctx, threadID)
}

func (r *Repo) ListMessages(ctx context.Context, chatID string, opts MessageListOptions) ([]*Message, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return r.messages.ListMessages(ctx, chatID, opts, func(ctx context.Context, threadID string) ([]string, error) {
		contextWindows, err := r.ListContextWindowsByThread(ctx, threadID)
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(contextWindows))
		for i, cw := range contextWindows {
			ids[i] = cw.ID
		}
		return ids, nil
	})
}

func (r *Repo) GetLatestMessageInThread(ctx context.Context, threadID string) (*Message, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	return r.messages.GetLatestMessageInThread(ctx, threadID)
}

func (r *Repo) GetMaxContextSequenceInThread(ctx context.Context, threadID string) (int, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	maxSeq, err := r.messages.GetLatestContextSequenceByThread(ctx, threadID)
	if err != nil {
		return 0, fmt.Errorf("failed to get max context sequence: %w", err)
	}

	// If we have local context_windows, use the max sequence
	if maxSeq > 0 {
		return int(maxSeq), nil
	}

	// No local context_windows yet - check if this is a forked thread via Thread record
	// If so, inherit the parent's context_sequence at the fork point
	threadRecord, _, err := r.GetThreadWithParent(ctx, threadID)
	if err != nil {
		logging.Debug("[GetMaxContextSequenceInThread] Error getting thread, defaulting to 0",
			"threadID", threadID,
			"error", err)
		return 0, nil
	}

	if threadRecord != nil && threadRecord.ParentThreadID != nil && threadRecord.ForkAtContextWindowID != nil {
		// Get the parent's context_window sequence at the fork point
		parentCW, err := r.GetContextWindow(ctx, *threadRecord.ForkAtContextWindowID)
		if err == nil && parentCW != nil {
			logging.Debug("[GetMaxContextSequenceInThread] Inheriting context_sequence from parent fork point",
				"threadID", threadID,
				"parentContextWindowID", *threadRecord.ForkAtContextWindowID,
				"inheritedContextSeq", parentCW.Sequence)
			return parentCW.Sequence, nil
		}
		logging.Debug("[GetMaxContextSequenceInThread] Failed to get parent context_window at fork point",
			"threadID", threadID,
			"parentThreadID", *threadRecord.ParentThreadID,
			"forkContextWindowID", *threadRecord.ForkAtContextWindowID,
			"error", err)
	}

	return 0, nil
}

func (r *Repo) GetLatestMessageWithTokensInThread(ctx context.Context, threadID string, contextSequence int) (*Message, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	return r.messages.GetLatestMessageWithTokensInThread(ctx, threadID, contextSequence)
}

func (r *Repo) CountMessagesInThread(ctx context.Context, threadID string) (int, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	return r.messages.CountMessagesInThread(ctx, threadID)
}

// GetMessagesByContextWindow loads messages from a specific context window.
// This is used by the threads package for CW chain resolution.
func (r *Repo) GetMessagesByContextWindow(ctx context.Context, contextWindowID string, maxOrdinal *int64) ([]*Message, error) {
	// For now, use the legacy approach with thread + context_sequence
	// This will be updated when we migrate messages to use context_window_id directly
	cw, conversationID, _, _, err := r.GetContextWindowWithThread(ctx, contextWindowID)
	if err != nil {
		return nil, fmt.Errorf("failed to get context window: %w", err)
	}

	opts := MessageListOptions{
		Thread:          &cw.ThreadID,
		ContextSequence: &cw.Sequence,
	}

	msgs, err := r.ListMessages(ctx, conversationID, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}

	// Filter by maxOrdinal if specified
	if maxOrdinal != nil {
		var filtered []*Message
		for _, msg := range msgs {
			if msg.Ordinal <= *maxOrdinal {
				filtered = append(filtered, msg)
			}
		}
		return filtered, nil
	}

	return msgs, nil
}

// GetEffectiveMessageCount returns the message count for a chat thread.
// NOTE: Workflow fork context inheritance is separate - this counts local messages only.
func (r *Repo) GetEffectiveMessageCount(ctx context.Context, chatID, thread string) (int, error) {
	if chatID == "" {
		return 0, fmt.Errorf("chat ID cannot be empty")
	}
	if thread == "" {
		return 0, fmt.Errorf("thread cannot be empty")
	}

	return r.CountMessagesInThread(ctx, thread)
}

// ==================== Content Blocks ====================

func (r *Repo) CreateContentBlock(ctx context.Context, block *MessageContentBlock) error {
	if block == nil {
		return fmt.Errorf("content block cannot be nil")
	}
	if block.ID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}
	if block.MessageID == "" {
		return fmt.Errorf("content block MessageID cannot be empty")
	}
	return r.messages.CreateContentBlock(ctx, block)
}

func (r *Repo) CreateContentBlockIfNotExists(ctx context.Context, block *MessageContentBlock) error {
	if block == nil {
		return fmt.Errorf("content block cannot be nil")
	}
	if block.ID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}
	if block.MessageID == "" {
		return fmt.Errorf("content block MessageID cannot be empty")
	}
	return r.messages.CreateContentBlockIfNotExists(ctx, block)
}

func (r *Repo) GetContentBlock(ctx context.Context, id string) (*MessageContentBlock, error) {
	if id == "" {
		return nil, fmt.Errorf("content block ID cannot be empty")
	}
	return r.messages.GetContentBlock(ctx, id)
}

// GetContentBlockByToolCallID finds a content block by its tool_call_id
// Returns an error if the block is not found or if the tool_call_id is empty
func (r *Repo) GetContentBlockByToolCallID(ctx context.Context, toolCallID string) (*MessageContentBlock, error) {
	if toolCallID == "" {
		return nil, fmt.Errorf("tool call ID cannot be empty")
	}

	// Query for tool_call block with the given tool_call_id
	query := `SELECT id, message_id, position, block_type, content, tool_name, tool_input,
	                 tool_call_id, is_error, version, created_at, updated_at
	          FROM message_content_blocks
	          WHERE tool_call_id = ? AND block_type = ?
	          LIMIT 1`
	query = r.bindQuery(query)
	toolCallBlockType := int32(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL)

	var block MessageContentBlock
	var content, toolName, toolInput, toolCallIDResult *string
	var isError *bool
	var version *int64

	err := r.DB.QueryRowContext(ctx, query, toolCallID, toolCallBlockType).Scan(
		&block.ID,
		&block.MessageID,
		&block.Position,
		&block.BlockType,
		&content,
		&toolName,
		&toolInput,
		&toolCallIDResult,
		&isError,
		&version,
		&block.CreatedAt,
		&block.UpdatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("tool call not found: %s", toolCallID)
		}
		return nil, fmt.Errorf("failed to get content block by tool_call_id: %w", err)
	}

	// Assign nullable fields
	block.Content = content
	block.ToolName = toolName
	block.ToolInput = toolInput
	block.ToolCallID = toolCallIDResult
	block.IsError = isError
	if version != nil {
		intVer := int(*version)
		block.Version = &intVer
	}

	return &block, nil
}

func (r *Repo) ListContentBlocks(ctx context.Context, messageID string) ([]*MessageContentBlock, error) {
	if messageID == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}
	return r.messages.ListContentBlocks(ctx, messageID)
}

func (r *Repo) ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*MessageContentBlock, error) {
	if len(messageIDs) == 0 {
		return []*MessageContentBlock{}, nil
	}
	return r.messages.ListContentBlocksForMessages(ctx, messageIDs)
}

func (r *Repo) UpdateContentBlock(ctx context.Context, block *MessageContentBlock) error {
	if block == nil {
		return fmt.Errorf("content block cannot be nil")
	}
	if block.ID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}

	return r.messages.UpdateContentBlock(ctx, block)
}

func (r *Repo) AppendToContentBlock(ctx context.Context, blockID string, delta string) error {
	if blockID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}

	return r.messages.AppendToContentBlock(ctx, blockID, delta)
}

// AppendContentBlockDelta appends text delta to a content block's content field
func (r *Repo) AppendContentBlockDelta(ctx context.Context, chatID string, blockID string, delta string) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	if blockID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}
	if delta == "" {
		return nil // Nothing to append
	}

	query := `UPDATE message_content_blocks
		SET content = COALESCE(content, '') || ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`
	query = r.bindQuery(query)
	_, err := r.DB.ExecContext(ctx, query, delta, blockID)
	return err
}

// AppendToolInputDelta appends JSON delta to a content block's tool_input field
func (r *Repo) AppendToolInputDelta(ctx context.Context, chatID string, blockID string, delta string) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	if blockID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}
	if delta == "" {
		return nil // Nothing to append
	}

	query := `UPDATE message_content_blocks
		SET tool_input = COALESCE(tool_input, '') || ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`
	query = r.bindQuery(query)
	_, err := r.DB.ExecContext(ctx, query, delta, blockID)
	return err
}

// UpdateMessage updates a message (implements Repository interface)
func (r *Repo) UpdateMessage(ctx context.Context, msg *Message) error {
	if msg == nil {
		return fmt.Errorf("message cannot be nil")
	}
	if msg.ID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}

	return r.messages.UpdateMessage(ctx, msg)
}

// UpdateMessageFields updates a message with the provided map of field updates
func (r *Repo) UpdateMessageFields(ctx context.Context, messageID string, updates map[string]interface{}) error {
	if messageID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}
	if len(updates) == 0 {
		return nil // Nothing to update
	}

	// Build dynamic UPDATE query
	setClauses := []string{}
	args := []interface{}{}

	for field, value := range updates {
		setClauses = append(setClauses, field+" = ?")
		args = append(args, value)
	}

	// Always update the updated_at timestamp so WebSocket polling detects the change
	setClauses = append(setClauses, "updated_at = CURRENT_TIMESTAMP")

	// Add messageID as final parameter
	args = append(args, messageID)

	query := `UPDATE messages SET ` + strings.Join(setClauses, ", ") + ` WHERE id = ?`
	query = r.bindQuery(query)
	_, err := r.DB.ExecContext(ctx, query, args...)
	return err
}

// MarkMessageBlocksComplete marks all content blocks for a message as complete
// This is called when streaming finishes to update timestamps
// Note: streaming_state field has been removed - state is now computed from content blocks
func (r *Repo) MarkMessageBlocksComplete(ctx context.Context, messageID string) error {
	if messageID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}

	now := time.Now().UTC()
	query := `
		UPDATE message_content_blocks
		SET updated_at = ?
		WHERE message_id = ?
	`
	query = r.bindQuery(query)

	result, err := r.DB.ExecContext(ctx, query, now, messageID)
	if err != nil {
		return fmt.Errorf("failed to mark blocks complete: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err == nil && rowsAffected > 0 {
		logging.Debug("Marked message blocks as complete",
			"messageID", messageID,
			"blocksUpdated", rowsAffected)
	}

	return nil
}

// DeleteMessage deletes a message and cascades to content blocks (ON DELETE CASCADE)
func (r *Repo) DeleteMessage(ctx context.Context, messageID string) error {
	if messageID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}

	query := `DELETE FROM messages WHERE id = ?`
	query = r.bindQuery(query)
	_, err := r.DB.ExecContext(ctx, query, messageID)
	return err
}

// ==================== Approval Methods ====================

// CreateApproval creates a new approval record and emits an activity event if the activity changed.
func (r *Repo) CreateApproval(ctx context.Context, approval *Approval) error {
	if err := r.approvals.CreateApproval(ctx, approval); err != nil {
		return err
	}
	// Activity may have changed from RUNNING → AWAITING_INPUT
	return r.emitChatActivityIfChanged(ctx, approval.ChatID)
}

// GetApproval retrieves an approval by ID
func (r *Repo) GetApproval(ctx context.Context, id string) (*Approval, error) {
	return r.approvals.GetApproval(ctx, id)
}

// GetApprovalByEntityID retrieves an approval by its entity_id
func (r *Repo) GetApprovalByEntityID(ctx context.Context, entityID string) (*Approval, error) {
	return r.approvals.GetApprovalByEntityID(ctx, entityID)
}

// ListPendingApprovalsByChat retrieves all pending approvals for a chat
func (r *Repo) ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*Approval, error) {
	return r.approvals.ListPendingApprovalsByChat(ctx, chatID)
}

// ListApprovalsByChat retrieves all approvals for a chat (including resolved)
func (r *Repo) ListApprovalsByChat(ctx context.Context, chatID string) ([]*Approval, error) {
	return r.approvals.ListApprovalsByChat(ctx, chatID)
}

// UpdateApprovalStatus updates the status of an approval and emits an activity event if the activity changed.
func (r *Repo) UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string) error {
	var resolvedAt *time.Time
	if status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED) || status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED) {
		now := time.Now()
		resolvedAt = &now
	}

	if err := r.approvals.UpdateApprovalStatus(ctx, id, status, denialReason, actionTaken, metadata, resolvedAt); err != nil {
		return err
	}

	// Look up the approval to get chatID for activity emission
	approval, err := r.GetApproval(ctx, id)
	if err != nil {
		return err
	}
	// Activity may have changed from AWAITING_INPUT → RUNNING
	return r.emitChatActivityIfChanged(ctx, approval.ChatID)
}

// ==================== Stub implementations for remaining Repository methods ====================

// These will be implemented as needed - returning clear errors for now

func (r *Repo) CreateProject(ctx context.Context, project *Project) error {
	if project == nil {
		return fmt.Errorf("project cannot be nil")
	}
	if project.ID == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	if project.Name == "" {
		return fmt.Errorf("project name cannot be empty")
	}
	if project.Path == "" {
		return fmt.Errorf("project path cannot be empty")
	}

	return r.projects.CreateProject(ctx, project)
}

func (r *Repo) GetProject(ctx context.Context, id string) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}

	return r.projects.GetProject(ctx, id)
}

func (r *Repo) GetProjectByPath(ctx context.Context, path string) (*Project, error) {
	if path == "" {
		return nil, fmt.Errorf("project path cannot be empty")
	}

	return r.projects.GetProjectByPath(ctx, path)
}

func (r *Repo) GetProjectByPathAndUser(ctx context.Context, path, userID string) (*Project, error) {
	if path == "" {
		return nil, fmt.Errorf("project path cannot be empty")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	return r.projects.GetProjectByPathAndUser(ctx, path, userID)
}

func (r *Repo) GetProjectByRemoteURLAndUser(ctx context.Context, remoteURL, userID string) (*Project, error) {
	if remoteURL == "" {
		return nil, fmt.Errorf("remote URL cannot be empty")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	return r.projects.GetProjectByRemoteURLAndUser(ctx, remoteURL, userID)
}

func (r *Repo) GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	return r.projects.GetProjectWithUserCheck(ctx, id, userID)
}

func (r *Repo) ListProjects(ctx context.Context, filters ProjectFilters) ([]*Project, error) {
	return r.projects.ListProjects(ctx, filters)
}

func (r *Repo) UpdateProject(ctx context.Context, project *Project, userID string) error {
	if project == nil {
		return fmt.Errorf("project cannot be nil")
	}
	if project.ID == "" {
		return fmt.Errorf("project ID cannot be empty")
	}

	return r.projects.UpdateProject(ctx, project, userID)
}

func (r *Repo) TouchProject(ctx context.Context, id string, userID string) error {
	if id == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	return r.projects.TouchProject(ctx, id, userID)
}

func (r *Repo) DeleteProject(ctx context.Context, id string, userID string) error {
	if id == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	return r.projects.DeleteProject(ctx, id, userID)
}

// =============================================================================
// Project ↔ Daemon installations
// =============================================================================

func (r *Repo) UpsertProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string) error {
	if projectID == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}
	return r.projects.UpsertProjectDaemon(ctx, projectID, daemonID, path, defaultBranch)
}

func (r *Repo) ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return r.projects.ListProjectDaemonsForProject(ctx, projectID)
}

func (r *Repo) ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemon, error) {
	if daemonID == "" {
		return nil, fmt.Errorf("daemon ID cannot be empty")
	}
	return r.projects.ListProjectDaemonsForDaemon(ctx, daemonID)
}

func (r *Repo) DeleteProjectDaemon(ctx context.Context, projectID, daemonID string) error {
	if projectID == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	return r.projects.DeleteProjectDaemon(ctx, projectID, daemonID)
}

func (r *Repo) UpsertDaemon(ctx context.Context, daemon *Daemon) error {
	if daemon == nil {
		return fmt.Errorf("daemon cannot be nil")
	}
	if daemon.ID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	if daemon.UserID == "" {
		return fmt.Errorf("daemon user ID cannot be empty")
	}

	now := time.Now().UTC()
	query := `
		INSERT INTO daemons (
			id,
			user_id,
			hostname,
			platform,
			capabilities,
			project_paths,
			daemon_type,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			user_id = excluded.user_id,
			hostname = COALESCE(excluded.hostname, daemons.hostname),
			platform = COALESCE(excluded.platform, daemons.platform),
			capabilities = COALESCE(excluded.capabilities, daemons.capabilities),
			project_paths = COALESCE(excluded.project_paths, daemons.project_paths),
			daemon_type = COALESCE(excluded.daemon_type, daemons.daemon_type),
			updated_at = excluded.updated_at
	`
	query = r.bindQuery(query)

	createdAt := daemon.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}

	_, err := r.DB.ExecContext(ctx, query,
		daemon.ID,
		daemon.UserID,
		daemon.Hostname,
		daemon.Platform,
		daemon.Capabilities,
		daemon.ProjectPaths,
		daemon.DaemonType,
		createdAt,
		now,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert daemon %s: %w", daemon.ID, err)
	}

	return nil
}

func (r *Repo) GetDaemon(ctx context.Context, id string) (*Daemon, error) {
	if id == "" {
		return nil, fmt.Errorf("daemon ID cannot be empty")
	}

	query := `
		SELECT
			id,
			user_id,
			hostname,
			platform,
			capabilities,
			project_paths,
			daemon_type,
			created_at,
			updated_at
		FROM daemons
		WHERE id = ?
		LIMIT 1
	`
	query = r.bindQuery(query)

	var (
		daemon       Daemon
		hostname     sql.NullString
		platform     sql.NullString
		capabilities sql.NullString
		projectPaths sql.NullString
		daemonType   sql.NullString
	)

	err := r.DB.QueryRowContext(ctx, query, id).Scan(
		&daemon.ID,
		&daemon.UserID,
		&hostname,
		&platform,
		&capabilities,
		&projectPaths,
		&daemonType,
		&daemon.CreatedAt,
		&daemon.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("failed to get daemon %s: %w", id, err)
	}

	daemon.Hostname = nullStringToPtr(hostname)
	daemon.Platform = nullStringToPtr(platform)
	daemon.Capabilities = nullStringToPtr(capabilities)
	daemon.ProjectPaths = nullStringToPtr(projectPaths)
	daemon.DaemonType = nullStringToPtr(daemonType)

	return &daemon, nil
}

func (r *Repo) ListDaemonsByUserID(ctx context.Context, userID string) ([]*Daemon, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	query := `
		SELECT
			id,
			user_id,
			hostname,
			platform,
			capabilities,
			project_paths,
			daemon_type,
			created_at,
			updated_at
		FROM daemons
		WHERE user_id = ?
		ORDER BY updated_at DESC
	`
	query = r.bindQuery(query)

	rows, err := r.DB.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list daemons for user %s: %w", userID, err)
	}
	defer rows.Close()

	var result []*Daemon
	for rows.Next() {
		var (
			daemon       Daemon
			hostname     sql.NullString
			platform     sql.NullString
			capabilities sql.NullString
			projectPaths sql.NullString
			daemonType   sql.NullString
		)

		if err := rows.Scan(
			&daemon.ID,
			&daemon.UserID,
			&hostname,
			&platform,
			&capabilities,
			&projectPaths,
			&daemonType,
			&daemon.CreatedAt,
			&daemon.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan daemon row: %w", err)
		}

		daemon.Hostname = nullStringToPtr(hostname)
		daemon.Platform = nullStringToPtr(platform)
		daemon.Capabilities = nullStringToPtr(capabilities)
		daemon.ProjectPaths = nullStringToPtr(projectPaths)
		daemon.DaemonType = nullStringToPtr(daemonType)

		result = append(result, &daemon)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed iterating daemon rows: %w", err)
	}

	return result, nil
}

func (r *Repo) UpsertDaemonAttachment(ctx context.Context, att *DaemonAttachment) error {
	if att == nil {
		return fmt.Errorf("daemon attachment cannot be nil")
	}
	if att.DaemonID == "" {
		return fmt.Errorf("daemon attachment requires daemon_id")
	}
	if att.UserID == "" {
		return fmt.Errorf("daemon attachment requires user_id")
	}
	if att.Source == "" {
		return fmt.Errorf("daemon attachment requires source")
	}
	now := time.Now().UTC()
	if att.AttachedAt.IsZero() {
		att.AttachedAt = now
	}
	if att.LastStreamActivity.IsZero() {
		att.LastStreamActivity = now
	}
	// Re-attachment resets memory telemetry and detected ports: a new stream
	// means a fresh daemon session, and readings from the previous one are
	// stale.
	query := `
		INSERT INTO daemon_attachment (daemon_id, user_id, source, pod_ip, pod_port, attached_at, last_stream_activity)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (daemon_id) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			source = EXCLUDED.source,
			pod_ip = EXCLUDED.pod_ip,
			pod_port = EXCLUDED.pod_port,
			attached_at = EXCLUDED.attached_at,
			last_stream_activity = EXCLUDED.last_stream_activity,
			memory_used_bytes = 0,
			memory_limit_bytes = 0,
			memory_pressure = FALSE,
			detected_ports = '[]'
	`
	query = r.bindQuery(query)
	if _, err := r.DB.ExecContext(ctx, query, att.DaemonID, att.UserID, string(att.Source), att.PodIP, att.PodPort, att.AttachedAt, att.LastStreamActivity); err != nil {
		return fmt.Errorf("upserting daemon attachment: %w", err)
	}
	return nil
}

// TouchDaemonAttachmentIfNewer advances last_stream_activity only when the
// incoming timestamp is strictly newer than what's already stored. The
// derivation consumer is the sole caller; the guard makes out-of-order NATS
// delivery a no-op rather than a regression.
func (r *Repo) TouchDaemonAttachmentIfNewer(ctx context.Context, daemonID string, activityAt time.Time) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	query := `UPDATE daemon_attachment SET last_stream_activity = ? WHERE daemon_id = ? AND last_stream_activity < ?`
	query = r.bindQuery(query)
	if _, err := r.DB.ExecContext(ctx, query, activityAt, daemonID, activityAt); err != nil {
		return fmt.Errorf("touching daemon attachment: %w", err)
	}
	return nil
}

// UpdateDaemonAttachmentMemory records the workspace memory telemetry carried
// by a daemon heartbeat on the attachment (liveness) record. A no-op when the
// attachment row doesn't exist — telemetry is only meaningful for a live
// stream. It deliberately does NOT touch last_stream_activity; the lease
// renewal stays with TouchDaemonAttachmentIfNewer.
func (r *Repo) UpdateDaemonAttachmentMemory(ctx context.Context, daemonID string, usedBytes, limitBytes int64, pressure bool) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	query := `UPDATE daemon_attachment SET memory_used_bytes = ?, memory_limit_bytes = ?, memory_pressure = ? WHERE daemon_id = ?`
	query = r.bindQuery(query)
	if _, err := r.DB.ExecContext(ctx, query, usedBytes, limitBytes, pressure, daemonID); err != nil {
		return fmt.Errorf("updating daemon attachment memory: %w", err)
	}
	return nil
}

// UpdateDaemonAttachmentPorts records the heartbeat-reported detected
// listener ports on the attachment (liveness) record. Mirrors
// UpdateDaemonAttachmentMemory: a no-op when the attachment row doesn't
// exist, and it deliberately does NOT touch last_stream_activity — the lease
// renewal stays with TouchDaemonAttachmentIfNewer.
func (r *Repo) UpdateDaemonAttachmentPorts(ctx context.Context, daemonID string, ports []uint32) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	if ports == nil {
		ports = []uint32{}
	}
	encoded, err := json.Marshal(ports)
	if err != nil {
		return fmt.Errorf("encoding detected ports: %w", err)
	}
	query := `UPDATE daemon_attachment SET detected_ports = ? WHERE daemon_id = ?`
	query = r.bindQuery(query)
	if _, err := r.DB.ExecContext(ctx, query, string(encoded), daemonID); err != nil {
		return fmt.Errorf("updating daemon attachment ports: %w", err)
	}
	return nil
}

func (r *Repo) DeleteDaemonAttachment(ctx context.Context, daemonID string) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	query := `DELETE FROM daemon_attachment WHERE daemon_id = ?`
	query = r.bindQuery(query)
	if _, err := r.DB.ExecContext(ctx, query, daemonID); err != nil {
		return fmt.Errorf("deleting daemon attachment: %w", err)
	}
	return nil
}

func (r *Repo) IsDaemonAttached(ctx context.Context, userID string, staleThreshold time.Duration) (bool, error) {
	if userID == "" {
		return false, fmt.Errorf("user ID cannot be empty")
	}
	cutoff := time.Now().UTC().Add(-staleThreshold)
	query := `SELECT 1 FROM daemon_attachment WHERE user_id = ? AND last_stream_activity > ? LIMIT 1`
	query = r.bindQuery(query)
	var n int
	err := r.DB.QueryRowContext(ctx, query, userID, cutoff).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking daemon attachment: %w", err)
	}
	return true, nil
}

// attachmentColumns is the shared SELECT column list matching
// listAttachments' scan order.
const attachmentColumns = `daemon_id, user_id, source, pod_ip, pod_port, attached_at, last_stream_activity, memory_used_bytes, memory_limit_bytes, memory_pressure, detected_ports`

func (r *Repo) ListOutboundAttachments(ctx context.Context) ([]*DaemonAttachment, error) {
	return r.listAttachments(ctx, `
		SELECT `+attachmentColumns+`
		FROM daemon_attachment
		WHERE source = 'outbound'
	`)
}

// ListAllDaemonAttachments returns every attachment row regardless of source.
func (r *Repo) ListAllDaemonAttachments(ctx context.Context) ([]*DaemonAttachment, error) {
	return r.listAttachments(ctx, `
		SELECT `+attachmentColumns+`
		FROM daemon_attachment
	`)
}

// ListFreshDaemonAttachmentsForUser returns the user's attachment rows whose
// last_stream_activity is within the stale threshold — the full liveness
// records (including memory telemetry) behind ListAttachedDaemonIDsForUser's
// id-only view.
func (r *Repo) ListFreshDaemonAttachmentsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]*DaemonAttachment, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	cutoff := time.Now().UTC().Add(-staleThreshold)
	return r.listAttachments(ctx, `
		SELECT `+attachmentColumns+`
		FROM daemon_attachment
		WHERE user_id = ? AND last_stream_activity > ?
	`, userID, cutoff)
}

// listAttachments runs an attachment-shaped SELECT and scans the rows. Shared
// by the attachment list methods so the scan logic (nullable pod_ip /
// pod_port) lives in one place.
func (r *Repo) listAttachments(ctx context.Context, query string, args ...any) ([]*DaemonAttachment, error) {
	query = r.bindQuery(query)
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing attachments: %w", err)
	}
	defer rows.Close()

	var result []*DaemonAttachment
	for rows.Next() {
		var (
			att           DaemonAttachment
			source        string
			podIP         sql.NullString
			podPort       sql.NullInt64
			detectedPorts string
		)
		if err := rows.Scan(&att.DaemonID, &att.UserID, &source, &podIP, &podPort, &att.AttachedAt, &att.LastStreamActivity, &att.MemoryUsedBytes, &att.MemoryLimitBytes, &att.MemoryPressure, &detectedPorts); err != nil {
			return nil, fmt.Errorf("scanning attachment: %w", err)
		}
		att.Source = DaemonAttachmentSource(source)
		if podIP.Valid {
			v := podIP.String
			att.PodIP = &v
		}
		if podPort.Valid {
			v := int(podPort.Int64)
			att.PodPort = &v
		}
		// detected_ports is JSON-encoded; a decode failure degrades to "no
		// ports" rather than failing the liveness read.
		if detectedPorts != "" && detectedPorts != "[]" {
			_ = json.Unmarshal([]byte(detectedPorts), &att.DetectedPorts)
		}
		result = append(result, &att)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating attachments: %w", err)
	}
	return result, nil
}

func (r *Repo) ListAttachedDaemonIDsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]string, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	cutoff := time.Now().UTC().Add(-staleThreshold)
	query := `SELECT daemon_id FROM daemon_attachment WHERE user_id = ? AND last_stream_activity > ?`
	query = r.bindQuery(query)
	rows, err := r.DB.QueryContext(ctx, query, userID, cutoff)
	if err != nil {
		return nil, fmt.Errorf("listing attached daemon ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning attached daemon id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating attached daemon ids: %w", err)
	}
	return ids, nil
}

func (r *Repo) UpsertProjectConfigRecord(ctx context.Context, record *ProjectConfigRecord) error {
	if record == nil {
		return fmt.Errorf("project config record cannot be nil")
	}
	if record.ID == "" {
		record.ID = uuid.New().String()
	}
	if record.ProjectID == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	if record.DaemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}

	now := time.Now().UTC()
	pushedAt := record.PushedAt
	if pushedAt.IsZero() {
		pushedAt = now
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}

	query := `
		INSERT INTO project_configs (
			id,
			project_id,
			daemon_id,
			user_config_yaml,
			project_config_yaml,
			local_config_yaml,
			global_memory_md,
			project_memory_md,
			mcp_configs,
			project_workflows_json,
			project_presets_json,
			project_scenarios_json,
			project_skills_json,
			repo_memories_json,
			runtime_type,
			pushed_at,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			daemon_id = excluded.daemon_id,
			user_config_yaml = excluded.user_config_yaml,
			project_config_yaml = excluded.project_config_yaml,
			local_config_yaml = excluded.local_config_yaml,
			global_memory_md = excluded.global_memory_md,
			project_memory_md = excluded.project_memory_md,
			mcp_configs = excluded.mcp_configs,
			project_workflows_json = excluded.project_workflows_json,
			project_presets_json = excluded.project_presets_json,
			project_scenarios_json = excluded.project_scenarios_json,
			project_skills_json = excluded.project_skills_json,
			repo_memories_json = excluded.repo_memories_json,
			runtime_type = excluded.runtime_type,
			pushed_at = excluded.pushed_at,
			updated_at = excluded.updated_at
	`
	query = r.bindQuery(query)

	_, err := r.DB.ExecContext(ctx, query,
		record.ID,
		record.ProjectID,
		record.DaemonID,
		record.UserConfigYAML,
		record.ProjectConfigYAML,
		record.LocalConfigYAML,
		record.GlobalMemoryMD,
		record.ProjectMemoryMD,
		record.MCPConfigs,
		record.ProjectWorkflowsJSON,
		record.ProjectPresetsJSON,
		record.ProjectScenariosJSON,
		record.ProjectSkillsJSON,
		record.RepoMemoriesJSON,
		record.RuntimeType,
		pushedAt,
		createdAt,
		now,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert project config record for project %s: %w", record.ProjectID, err)
	}

	return nil
}

func (r *Repo) GetProjectConfigRecord(ctx context.Context, projectID string) (*ProjectConfigRecord, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}

	query := `
		SELECT
			id,
			project_id,
			daemon_id,
			user_config_yaml,
			project_config_yaml,
			local_config_yaml,
			global_memory_md,
			project_memory_md,
			mcp_configs,
			project_workflows_json,
			project_presets_json,
			project_scenarios_json,
			project_skills_json,
			repo_memories_json,
			runtime_type,
			pushed_at,
			created_at,
			updated_at
		FROM project_configs
		WHERE project_id = ?
		LIMIT 1
	`
	query = r.bindQuery(query)

	var record ProjectConfigRecord
	err := r.DB.QueryRowContext(ctx, query, projectID).Scan(
		&record.ID,
		&record.ProjectID,
		&record.DaemonID,
		&record.UserConfigYAML,
		&record.ProjectConfigYAML,
		&record.LocalConfigYAML,
		&record.GlobalMemoryMD,
		&record.ProjectMemoryMD,
		&record.MCPConfigs,
		&record.ProjectWorkflowsJSON,
		&record.ProjectPresetsJSON,
		&record.ProjectScenariosJSON,
		&record.ProjectSkillsJSON,
		&record.RepoMemoriesJSON,
		&record.RuntimeType,
		&record.PushedAt,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("failed to load project config record for project %s: %w", projectID, err)
	}

	return &record, nil
}

// ==================== Daemon PATs ====================

func (r *Repo) CreateDaemonPAT(ctx context.Context, pat *DaemonPAT) error {
	if pat == nil {
		return fmt.Errorf("pat cannot be nil")
	}
	if pat.ID == "" {
		return fmt.Errorf("pat ID cannot be empty")
	}
	if pat.UserID == "" {
		return fmt.Errorf("pat user ID cannot be empty")
	}
	if pat.TokenHash == "" {
		return fmt.Errorf("pat token hash cannot be empty")
	}

	query := `
		INSERT INTO daemon_pats (id, user_id, daemon_id, token_hash, token_prefix, name, ephemeral, expires_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)
	`
	query = r.bindQuery(query)

	createdAt := pat.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	var daemonID *string
	if pat.DaemonID != "" {
		daemonID = &pat.DaemonID
	}

	_, err := r.DB.ExecContext(ctx, query,
		pat.ID,
		pat.UserID,
		daemonID,
		pat.TokenHash,
		pat.TokenPrefix,
		pat.Name,
		pat.Ephemeral,
		pat.ExpiresAt,
		createdAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create daemon PAT %s: %w", pat.ID, err)
	}

	return nil
}

func (r *Repo) GetDaemonPATByTokenHash(ctx context.Context, tokenHash string) (*DaemonPAT, error) {
	if tokenHash == "" {
		return nil, fmt.Errorf("token hash cannot be empty")
	}

	query := `
		SELECT id, user_id, COALESCE(daemon_id, ''), token_hash, token_prefix, name, ephemeral, expires_at, last_used_at, revoked_at, created_at
		FROM daemon_pats
		WHERE token_hash = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
	`
	query = r.bindQuery(query)

	var (
		pat        DaemonPAT
		ephemeral  sql.NullBool
		expiresAt  sql.NullTime
		lastUsedAt sql.NullTime
		revokedAt  sql.NullTime
	)

	err := r.DB.QueryRowContext(ctx, query, tokenHash).Scan(
		&pat.ID,
		&pat.UserID,
		&pat.DaemonID,
		&pat.TokenHash,
		&pat.TokenPrefix,
		&pat.Name,
		&ephemeral,
		&expiresAt,
		&lastUsedAt,
		&revokedAt,
		&pat.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("failed to get daemon PAT by token hash: %w", err)
	}

	pat.Ephemeral = ephemeral.Valid && ephemeral.Bool
	pat.ExpiresAt = nullTimeToPtr(expiresAt)
	pat.LastUsedAt = nullTimeToPtr(lastUsedAt)
	pat.RevokedAt = nullTimeToPtr(revokedAt)

	return &pat, nil
}

func (r *Repo) ListDaemonPATsByUserID(ctx context.Context, userID string) ([]*DaemonPAT, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	query := `
		SELECT id, user_id, COALESCE(daemon_id, ''), token_hash, token_prefix, name, ephemeral, expires_at, last_used_at, revoked_at, created_at
		FROM daemon_pats
		WHERE user_id = ?
		ORDER BY created_at DESC
	`
	query = r.bindQuery(query)

	rows, err := r.DB.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list daemon PATs for user %s: %w", userID, err)
	}
	defer rows.Close()

	var pats []*DaemonPAT
	for rows.Next() {
		var (
			pat        DaemonPAT
			ephemeral  sql.NullBool
			expiresAt  sql.NullTime
			lastUsedAt sql.NullTime
			revokedAt  sql.NullTime
		)

		if err := rows.Scan(
			&pat.ID,
			&pat.UserID,
			&pat.DaemonID,
			&pat.TokenHash,
			&pat.TokenPrefix,
			&pat.Name,
			&ephemeral,
			&expiresAt,
			&lastUsedAt,
			&revokedAt,
			&pat.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan daemon PAT row: %w", err)
		}

		pat.Ephemeral = ephemeral.Valid && ephemeral.Bool
		pat.ExpiresAt = nullTimeToPtr(expiresAt)
		pat.LastUsedAt = nullTimeToPtr(lastUsedAt)
		pat.RevokedAt = nullTimeToPtr(revokedAt)

		pats = append(pats, &pat)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed iterating daemon PAT rows: %w", err)
	}

	return pats, nil
}

func (r *Repo) RevokeDaemonPAT(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("pat ID cannot be empty")
	}

	query := `UPDATE daemon_pats SET revoked_at = CURRENT_TIMESTAMP WHERE id = ? AND revoked_at IS NULL`
	query = r.bindQuery(query)

	_, err := r.DB.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to revoke daemon PAT %s: %w", id, err)
	}

	return nil
}

func (r *Repo) RevokeDaemonPATsByUserID(ctx context.Context, userID string, ephemeralOnly bool) error {
	if userID == "" {
		return fmt.Errorf("user ID cannot be empty")
	}

	query := `UPDATE daemon_pats SET revoked_at = CURRENT_TIMESTAMP WHERE user_id = ? AND revoked_at IS NULL`
	if ephemeralOnly {
		query += ` AND ephemeral = 1`
	}
	query = r.bindQuery(query)

	_, err := r.DB.ExecContext(ctx, query, userID)
	if err != nil {
		return fmt.Errorf("failed to revoke daemon PATs for user %s: %w", userID, err)
	}

	return nil
}

// RevokeDaemonPATsByDaemonID marks every live PAT bound to daemonID as revoked.
// daemon_id is nullable on the table; this only matches rows where it equals the
// supplied non-empty daemonID, so unbound (user-only) PATs are never touched.
// Returns the count of rows that transitioned from live to revoked.
func (r *Repo) RevokeDaemonPATsByDaemonID(ctx context.Context, daemonID string) (int, error) {
	if daemonID == "" {
		return 0, fmt.Errorf("daemon ID cannot be empty")
	}

	query := `UPDATE daemon_pats SET revoked_at = CURRENT_TIMESTAMP WHERE daemon_id = ? AND revoked_at IS NULL`
	query = r.bindQuery(query)

	res, err := r.DB.ExecContext(ctx, query, daemonID)
	if err != nil {
		return 0, fmt.Errorf("failed to revoke daemon PATs for daemon %s: %w", daemonID, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		// The statement succeeded; the driver just couldn't report the count.
		// Report 0 with no error rather than failing the revocation.
		return 0, nil
	}

	return int(n), nil
}

func (r *Repo) UpdateDaemonPATLastUsed(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("pat ID cannot be empty")
	}

	query := `UPDATE daemon_pats SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`
	query = r.bindQuery(query)

	_, err := r.DB.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to update daemon PAT last used for %s: %w", id, err)
	}

	return nil
}

func (r *Repo) CreateWorktree(ctx context.Context, worktree *Worktree) error {
	if worktree == nil {
		return fmt.Errorf("worktree cannot be nil")
	}
	if worktree.ID == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}
	if worktree.Name == "" {
		return fmt.Errorf("worktree name cannot be empty")
	}
	return r.worktrees.CreateWorktree(ctx, worktree)
}

func (r *Repo) GetWorktree(ctx context.Context, id string) (*Worktree, error) {
	if id == "" {
		return nil, fmt.Errorf("worktree ID cannot be empty")
	}
	return r.worktrees.GetWorktree(ctx, id)
}

func (r *Repo) GetWorktreeByPath(ctx context.Context, path string) (*Worktree, error) {
	if path == "" {
		return nil, fmt.Errorf("worktree path cannot be empty")
	}
	return r.worktrees.GetWorktreeByPath(ctx, path)
}

func (r *Repo) ListWorktrees(ctx context.Context, filters WorktreeFilters) ([]*Worktree, error) {
	return r.worktrees.ListWorktrees(ctx, filters)
}

func (r *Repo) UpdateWorktree(ctx context.Context, worktree *Worktree) error {
	if worktree == nil {
		return fmt.Errorf("worktree cannot be nil")
	}
	if worktree.ID == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}

	return r.worktrees.UpdateWorktree(ctx, worktree)
}

func (r *Repo) UpdateWorktreeCleanupMetadata(ctx context.Context, id string, metadata *CleanupMetadata) error {
	if id == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}

	return r.worktrees.UpdateWorktreeCleanupMetadata(ctx, id, metadata)
}

func (r *Repo) DeleteWorktree(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}
	return r.worktrees.DeleteWorktree(ctx, id)
}

func (r *Repo) ArchiveWorktree(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}
	return r.worktrees.ArchiveWorktree(ctx, id)
}

func (r *Repo) UnarchiveWorktree(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}
	return r.worktrees.UnarchiveWorktree(ctx, id)
}

// =============================================================================
// Repos (nested git repositories within a project)
// =============================================================================

func (r *Repo) CreateRepo(ctx context.Context, repo *core.Repo) error {
	if repo == nil {
		return fmt.Errorf("repo cannot be nil")
	}
	if repo.ID == "" {
		return fmt.Errorf("repo ID cannot be empty")
	}
	if repo.ProjectID == "" {
		return fmt.Errorf("repo project_id cannot be empty")
	}
	return r.repos.CreateRepo(ctx, repo)
}

func (r *Repo) GetRepo(ctx context.Context, id string) (*core.Repo, error) {
	if id == "" {
		return nil, fmt.Errorf("repo ID cannot be empty")
	}
	return r.repos.GetRepo(ctx, id)
}

func (r *Repo) GetRepoByProjectAndPath(ctx context.Context, projectID, relativePath string) (*core.Repo, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return r.repos.GetRepoByProjectAndPath(ctx, projectID, relativePath)
}

func (r *Repo) ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return r.repos.ListReposByProject(ctx, projectID)
}

func (r *Repo) UpdateRepo(ctx context.Context, repo *core.Repo) error {
	if repo == nil {
		return fmt.Errorf("repo cannot be nil")
	}
	if repo.ID == "" {
		return fmt.Errorf("repo ID cannot be empty")
	}
	return r.repos.UpdateRepo(ctx, repo)
}

func (r *Repo) DeleteRepo(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("repo ID cannot be empty")
	}
	return r.repos.DeleteRepo(ctx, id)
}

// Plans and Tasks methods are provided by the embedded pgdb.Querier

func (r *Repo) CreateSetting(ctx context.Context, setting *Setting) error {
	if setting == nil {
		return fmt.Errorf("setting cannot be nil")
	}
	if setting.ID == "" {
		return fmt.Errorf("setting ID cannot be empty")
	}
	if setting.UserID == "" {
		return fmt.Errorf("setting user_id cannot be empty")
	}
	if setting.Key == "" {
		return fmt.Errorf("setting key cannot be empty")
	}

	return r.settings.CreateSetting(ctx, setting)
}

func (r *Repo) GetSetting(ctx context.Context, userID string, projectID *string, key string) (*Setting, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}
	if key == "" {
		return nil, fmt.Errorf("setting key cannot be empty")
	}

	return r.settings.GetSetting(ctx, userID, projectID, key)
}

func (r *Repo) ListSettings(ctx context.Context, userID string, projectID *string) ([]*Setting, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}

	return r.settings.ListSettings(ctx, userID, projectID)
}

func (r *Repo) ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*Setting, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}
	if keyPattern == "" {
		return nil, fmt.Errorf("key pattern cannot be empty")
	}

	return r.settings.ListSettingsByKey(ctx, userID, keyPattern)
}

func (r *Repo) UpdateSetting(ctx context.Context, setting *Setting) error {
	if setting == nil {
		return fmt.Errorf("setting cannot be nil")
	}
	if setting.ID == "" {
		return fmt.Errorf("setting ID cannot be empty")
	}

	return r.settings.UpdateSetting(ctx, setting)
}

func (r *Repo) DeleteSetting(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("setting ID cannot be empty")
	}
	return r.settings.DeleteSetting(ctx, id)
}

// ==================== Settings Helper Methods ====================

func (r *Repo) GetStringOrDefault(ctx context.Context, userID string, projectID *string, key, defaultVal string) (string, error) {
	setting, err := r.GetSetting(ctx, userID, projectID, key)
	if err != nil {
		// If setting not found, return default
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "not found") {
			return defaultVal, nil
		}
		return "", err
	}
	return setting.Value, nil
}

func (r *Repo) GetBoolOrDefault(ctx context.Context, userID string, projectID *string, key string, defaultVal bool) (bool, error) {
	setting, err := r.GetSetting(ctx, userID, projectID, key)
	if err != nil {
		// If setting not found, return default
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "not found") {
			return defaultVal, nil
		}
		return false, err
	}
	val, err := strconv.ParseBool(setting.Value)
	if err != nil {
		return false, fmt.Errorf("failed to parse bool value: %w", err)
	}
	return val, nil
}

func (r *Repo) GetIntOrDefault(ctx context.Context, userID string, projectID *string, key string, defaultVal int) (int, error) {
	setting, err := r.GetSetting(ctx, userID, projectID, key)
	if err != nil {
		// If setting not found, return default
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "not found") {
			return defaultVal, nil
		}
		return 0, err
	}
	val, err := strconv.Atoi(setting.Value)
	if err != nil {
		return 0, fmt.Errorf("failed to parse int value: %w", err)
	}
	return val, nil
}

func (r *Repo) GetFloatOrDefault(ctx context.Context, userID string, projectID *string, key string, defaultVal float64) (float64, error) {
	setting, err := r.GetSetting(ctx, userID, projectID, key)
	if err != nil {
		// If setting not found, return default
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "not found") {
			return defaultVal, nil
		}
		return 0, err
	}
	val, err := strconv.ParseFloat(setting.Value, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse float value: %w", err)
	}
	return val, nil
}

func (r *Repo) SetString(ctx context.Context, userID string, projectID *string, key, value string) error {
	return r.upsertSetting(ctx, userID, projectID, key, value, "string")
}

func (r *Repo) SetBool(ctx context.Context, userID string, projectID *string, key string, value bool) error {
	return r.upsertSetting(ctx, userID, projectID, key, strconv.FormatBool(value), "bool")
}

func (r *Repo) SetInt(ctx context.Context, userID string, projectID *string, key string, value int) error {
	return r.upsertSetting(ctx, userID, projectID, key, strconv.Itoa(value), "int")
}

func (r *Repo) SetFloat(ctx context.Context, userID string, projectID *string, key string, value float64) error {
	return r.upsertSetting(ctx, userID, projectID, key, strconv.FormatFloat(value, 'f', -1, 64), "float")
}

func (r *Repo) upsertSetting(ctx context.Context, userID string, projectID *string, key, value, valueType string) error {
	setting, err := r.GetSetting(ctx, userID, projectID, key)
	if err != nil {
		// If not found, create new setting
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "not found") {
			newSetting := &Setting{
				ID:        generateID(),
				UserID:    userID,
				ProjectID: projectID,
				Key:       key,
				Value:     value,
				ValueType: valueType,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			return r.CreateSetting(ctx, newSetting)
		}
		return err
	}

	// Update existing setting
	setting.Value = value
	setting.ValueType = valueType
	return r.UpdateSetting(ctx, setting)
}

func (r *Repo) GetProviderAPIKey(ctx context.Context, userID string, provider string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("user_id cannot be empty")
	}
	if provider == "" {
		return "", fmt.Errorf("provider cannot be empty")
	}

	return r.settings.GetProviderAPIKey(ctx, userID, provider)
}

func (r *Repo) SetProviderAPIKey(ctx context.Context, userID string, provider, apiKey string) error {
	if userID == "" {
		return fmt.Errorf("user_id cannot be empty")
	}
	if provider == "" {
		return fmt.Errorf("provider cannot be empty")
	}

	return r.settings.SetProviderAPIKey(ctx, userID, provider, apiKey)
}

func (r *Repo) DeleteProviderAPIKey(ctx context.Context, userID string, provider string) error {
	if userID == "" {
		return fmt.Errorf("user_id cannot be empty")
	}
	if provider == "" {
		return fmt.Errorf("provider cannot be empty")
	}

	return r.settings.DeleteProviderAPIKey(ctx, userID, provider)
}

func (r *Repo) GetProviderAPIKeys(ctx context.Context, userID string) (map[string]string, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}

	return r.settings.GetProviderAPIKeys(ctx, userID)
}

func (r *Repo) GetCodexAuthTokens(ctx context.Context, userID string) (*CodexAuthTokens, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}

	return r.settings.GetCodexAuthTokens(ctx, userID)
}

func (r *Repo) SetCodexAuthTokens(ctx context.Context, userID string, tokens CodexAuthTokens) error {
	if userID == "" {
		return fmt.Errorf("user_id cannot be empty")
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return fmt.Errorf("access token cannot be empty")
	}

	return r.settings.SetCodexAuthTokens(ctx, userID, tokens)
}

func (r *Repo) DeleteCodexAuthTokens(ctx context.Context, userID string) error {
	if userID == "" {
		return fmt.Errorf("user_id cannot be empty")
	}

	return r.settings.DeleteCodexAuthTokens(ctx, userID)
}

func (r *Repo) GetCopilotAuthTokens(ctx context.Context, userID string) (*CopilotAuthTokens, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}

	return r.settings.GetCopilotAuthTokens(ctx, userID)
}

func (r *Repo) SetCopilotAuthTokens(ctx context.Context, userID string, tokens CopilotAuthTokens) error {
	if userID == "" {
		return fmt.Errorf("user_id cannot be empty")
	}
	if strings.TrimSpace(tokens.GitHubAccessToken) == "" {
		return fmt.Errorf("github access token cannot be empty")
	}

	return r.settings.SetCopilotAuthTokens(ctx, userID, tokens)
}

func (r *Repo) DeleteCopilotAuthTokens(ctx context.Context, userID string) error {
	if userID == "" {
		return fmt.Errorf("user_id cannot be empty")
	}

	return r.settings.DeleteCopilotAuthTokens(ctx, userID)
}

func (r *Repo) GetClaudeAuthTokens(ctx context.Context, userID string) (*ClaudeAuthTokens, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}

	return r.settings.GetClaudeAuthTokens(ctx, userID)
}

func (r *Repo) SetClaudeAuthTokens(ctx context.Context, userID string, tokens ClaudeAuthTokens) error {
	if userID == "" {
		return fmt.Errorf("user_id cannot be empty")
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return fmt.Errorf("access token cannot be empty")
	}

	return r.settings.SetClaudeAuthTokens(ctx, userID, tokens)
}

// CompareAndSwapClaudeAuthTokens persists refreshed tokens only when the
// stored refresh token still equals expectedRefreshToken (i.e. no concurrent
// rotation was persisted in between). Returns true when the write happened.
func (r *Repo) CompareAndSwapClaudeAuthTokens(ctx context.Context, userID string, expectedRefreshToken string, tokens ClaudeAuthTokens) (bool, error) {
	if userID == "" {
		return false, fmt.Errorf("user_id cannot be empty")
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return false, fmt.Errorf("access token cannot be empty")
	}

	return r.settings.CompareAndSwapClaudeAuthTokens(ctx, userID, expectedRefreshToken, tokens)
}

func (r *Repo) DeleteClaudeAuthTokens(ctx context.Context, userID string) error {
	if userID == "" {
		return fmt.Errorf("user_id cannot be empty")
	}

	return r.settings.DeleteClaudeAuthTokens(ctx, userID)
}

// ==================== Visibility Methods ====================

// GetVisibilityOverride returns the user's visibility override for an item, or nil if none exists.
func (r *Repo) GetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) (*bool, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}
	if itemType != 1 && itemType != 2 { // HIDDEN_ITEM_TYPE_WORKFLOW=1, HIDDEN_ITEM_TYPE_PRESET=2
		return nil, fmt.Errorf("item_type must be WORKFLOW (1) or PRESET (2)")
	}
	if slug == "" {
		return nil, fmt.Errorf("slug cannot be empty")
	}

	return r.settings.GetVisibilityOverride(ctx, userID, itemType, slug)
}

// ListVisibilityOverrides returns all user overrides for a given item type.
func (r *Repo) ListVisibilityOverrides(ctx context.Context, userID string, itemType int32) (map[string]bool, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}
	if itemType != 1 && itemType != 2 { // HIDDEN_ITEM_TYPE_WORKFLOW=1, HIDDEN_ITEM_TYPE_PRESET=2
		return nil, fmt.Errorf("item_type must be WORKFLOW (1) or PRESET (2)")
	}

	return r.settings.ListVisibilityOverrides(ctx, userID, itemType)
}

// SetVisibilityOverride sets a user's visibility preference for an item.
func (r *Repo) SetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string, isVisible bool) error {
	if userID == "" {
		return fmt.Errorf("user_id cannot be empty")
	}
	if itemType != 1 && itemType != 2 { // HIDDEN_ITEM_TYPE_WORKFLOW=1, HIDDEN_ITEM_TYPE_PRESET=2
		return fmt.Errorf("item_type must be WORKFLOW (1) or PRESET (2)")
	}
	if slug == "" {
		return fmt.Errorf("slug cannot be empty")
	}

	return r.settings.SetVisibilityOverride(ctx, userID, itemType, slug, isVisible)
}

// DeleteVisibilityOverride removes a user's visibility override, reverting to default.
func (r *Repo) DeleteVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) error {
	if userID == "" {
		return fmt.Errorf("user_id cannot be empty")
	}
	if itemType != 1 && itemType != 2 { // HIDDEN_ITEM_TYPE_WORKFLOW=1, HIDDEN_ITEM_TYPE_PRESET=2
		return fmt.Errorf("item_type must be WORKFLOW (1) or PRESET (2)")
	}
	if slug == "" {
		return fmt.Errorf("slug cannot be empty")
	}

	return r.settings.DeleteVisibilityOverride(ctx, userID, itemType, slug)
}

// GetItemDefault returns the system default visibility for an item.
func (r *Repo) GetItemDefault(ctx context.Context, itemType int32, slug string) (*ItemDefault, error) {
	if itemType != 1 && itemType != 2 { // HIDDEN_ITEM_TYPE_WORKFLOW=1, HIDDEN_ITEM_TYPE_PRESET=2
		return nil, fmt.Errorf("item_type must be WORKFLOW (1) or PRESET (2)")
	}
	if slug == "" {
		return nil, fmt.Errorf("slug cannot be empty")
	}

	return r.settings.GetItemDefault(ctx, itemType, slug)
}

// ListHiddenItemDefaults returns all items that are hidden by default.
func (r *Repo) ListHiddenItemDefaults(ctx context.Context, itemType int32) ([]string, error) {
	if itemType != 1 && itemType != 2 { // HIDDEN_ITEM_TYPE_WORKFLOW=1, HIDDEN_ITEM_TYPE_PRESET=2
		return nil, fmt.Errorf("item_type must be WORKFLOW (1) or PRESET (2)")
	}

	return r.settings.ListHiddenItemDefaults(ctx, itemType)
}

// IsItemVisible determines if an item should be visible to a user.
// Priority: user override > system default > visible (if no default exists)
func (r *Repo) IsItemVisible(ctx context.Context, userID string, itemType int32, slug string) (bool, error) {
	// Check user override first
	override, err := r.GetVisibilityOverride(ctx, userID, itemType, slug)
	if err != nil {
		return false, err
	}
	if override != nil {
		return *override, nil
	}

	// Check system default
	itemDefault, err := r.GetItemDefault(ctx, itemType, slug)
	if err == nil && itemDefault != nil {
		return !itemDefault.IsHidden, nil
	}

	// No override or default = visible
	return true, nil
}

// GetDefaultPresetAssignments returns the default preset assignments for a workflow.
// Returns a map of group name to preset slug.
func (r *Repo) GetDefaultPresetAssignments(ctx context.Context, workflowName string) (map[string]string, error) {
	if workflowName == "" {
		return nil, fmt.Errorf("workflow_name cannot be empty")
	}

	return r.settings.GetDefaultPresetAssignments(ctx, workflowName)
}

func (r *Repo) CreatePlan(ctx context.Context, plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("plan cannot be nil")
	}
	if plan.ThreadID == "" {
		return fmt.Errorf("thread ID is required")
	}
	if plan.Title == "" {
		return fmt.Errorf("title is required")
	}

	return r.planTasks.CreatePlan(ctx, plan)
}

func (r *Repo) GetPlan(ctx context.Context, id string) (*Plan, error) {
	if id == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}

	return r.planTasks.GetPlan(ctx, id)
}

func (r *Repo) GetPlanByThreadID(ctx context.Context, threadID string) (*Plan, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	return r.planTasks.GetPlanByThreadID(ctx, threadID)
}

func (r *Repo) ListPlansByThread(ctx context.Context, threadID string) ([]*Plan, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	return r.planTasks.ListPlansByThread(ctx, threadID)
}

func (r *Repo) ListPlansByChatID(ctx context.Context, chatID string) ([]*Plan, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return r.planTasks.ListPlansByChatID(ctx, chatID)
}

func (r *Repo) ListPlansByProject(ctx context.Context, projectID string) ([]*Plan, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}

	return r.planTasks.ListPlansByProject(ctx, projectID)
}

func (r *Repo) UpdatePlan(ctx context.Context, plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("plan cannot be nil")
	}
	if plan.ID == "" {
		return fmt.Errorf("plan ID cannot be empty")
	}

	return r.planTasks.UpdatePlan(ctx, plan)
}

func (r *Repo) UpdatePlanStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error {
	if id == "" {
		return fmt.Errorf("plan ID cannot be empty")
	}

	return r.planTasks.UpdatePlanStatus(ctx, id, status, completedAt)
}

func (r *Repo) DeletePlan(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("plan ID cannot be empty")
	}
	return r.planTasks.DeletePlan(ctx, id)
}

// ==================== Tasks ====================

func (r *Repo) CreateTask(ctx context.Context, task *Task) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}
	if task.PlanID == "" {
		return fmt.Errorf("plan ID is required")
	}
	if task.Title == "" {
		return fmt.Errorf("title is required")
	}

	return r.planTasks.CreateTask(ctx, task)
}

func (r *Repo) GetTask(ctx context.Context, id string) (*Task, error) {
	if id == "" {
		return nil, fmt.Errorf("task ID cannot be empty")
	}

	return r.planTasks.GetTask(ctx, id)
}

func (r *Repo) GetTaskByPosition(ctx context.Context, planID string, position int) (*Task, error) {
	if planID == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}

	// Get all tasks and return the one at the position
	tasks, err := r.ListTasksByPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if position < 0 || position >= len(tasks) {
		return nil, fmt.Errorf("position %d out of range", position)
	}
	return tasks[position], nil
}

func (r *Repo) ListTasksByPlan(ctx context.Context, planID string) ([]*Task, error) {
	if planID == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}

	return r.planTasks.ListTasksByPlan(ctx, planID)
}

func (r *Repo) ListTasksByParent(ctx context.Context, parentID string) ([]*Task, error) {
	if parentID == "" {
		return nil, fmt.Errorf("parent ID cannot be empty")
	}

	// We need to get all tasks and filter by parent
	// This is inefficient but works without a custom query
	return nil, fmt.Errorf("ListTasksByParent not implemented - needs custom query or plan ID")
}

func (r *Repo) ListRootTasksByPlan(ctx context.Context, planID string) ([]*Task, error) {
	if planID == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}

	// Get all tasks and filter for root tasks (no parent)
	allTasks, err := r.ListTasksByPlan(ctx, planID)
	if err != nil {
		return nil, err
	}

	rootTasks := []*Task{}
	for _, task := range allTasks {
		if task.ParentTaskID == nil {
			rootTasks = append(rootTasks, task)
		}
	}
	return rootTasks, nil
}

func (r *Repo) GetTaskStatsByPlan(ctx context.Context, planID string) (*TaskStats, error) {
	if planID == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}

	tasks, err := r.ListTasksByPlan(ctx, planID)
	if err != nil {
		return nil, err
	}

	stats := &TaskStats{
		Total: len(tasks),
	}

	for _, task := range tasks {
		switch TaskStatus(task.Status) {
		case TaskStatusPending:
			stats.Pending++
		case TaskStatusInProgress:
			stats.InProgress++
		case TaskStatusCompleted:
			stats.Completed++
		case TaskStatusFailed:
			stats.Failed++
		case TaskStatusBlocked:
			stats.Blocked++
		case TaskStatusCancelled:
			stats.Cancelled++
		case TaskStatusSkipped:
			stats.Skipped++
		}
	}

	return stats, nil
}

func (r *Repo) UpdateTask(ctx context.Context, task *Task) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}
	if task.ID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	return r.planTasks.UpdateTask(ctx, task)
}

func (r *Repo) UpdateTaskStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error {
	if id == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	// If marking as completed, set completedAt if not provided
	if completedAt == nil && status == int32(TaskStatusCompleted) {
		now := time.Now()
		completedAt = &now
	}

	return r.planTasks.UpdateTaskStatus(ctx, id, status, completedAt)
}

func (r *Repo) DeleteTask(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("task ID cannot be empty")
	}
	return r.planTasks.DeleteTask(ctx, id)
}

// ==================== Task Dependencies ====================

func (r *Repo) CreateTaskDependency(ctx context.Context, dep *TaskDependency) error {
	if dep == nil {
		return fmt.Errorf("dependency cannot be nil")
	}
	if dep.FromTaskID == "" || dep.ToTaskID == "" {
		return fmt.Errorf("from_task_id and to_task_id are required")
	}
	if dep.FromTaskID == dep.ToTaskID {
		return fmt.Errorf("a task cannot depend on itself")
	}
	return r.planTasks.CreateTaskDependency(ctx, dep)
}

func (r *Repo) GetTaskDependency(ctx context.Context, id string) (*TaskDependency, error) {
	if id == "" {
		return nil, fmt.Errorf("dependency ID cannot be empty")
	}
	return r.planTasks.GetTaskDependency(ctx, id)
}

func (r *Repo) ListTaskDependenciesByTask(ctx context.Context, taskID string) ([]*TaskDependency, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task ID cannot be empty")
	}
	return r.planTasks.ListTaskDependenciesByTask(ctx, taskID)
}

func (r *Repo) ListBlockersForTask(ctx context.Context, taskID string) ([]*TaskDependency, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task ID cannot be empty")
	}
	return r.planTasks.ListBlockersForTask(ctx, taskID)
}

func (r *Repo) ListDependenciesByPlan(ctx context.Context, planID string) ([]*TaskDependency, error) {
	if planID == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}
	return r.planTasks.ListDependenciesByPlan(ctx, planID)
}

func (r *Repo) DeleteTaskDependency(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("dependency ID cannot be empty")
	}
	return r.planTasks.DeleteTaskDependency(ctx, id)
}

func (r *Repo) DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error {
	if fromTaskID == "" || toTaskID == "" || depType == 0 {
		return fmt.Errorf("from_task_id, to_task_id, and dependency_type are required")
	}
	return r.planTasks.DeleteTaskDependencyByPair(ctx, fromTaskID, toTaskID, depType)
}

// ==================== Background Processes ====================

func (r *Repo) CreateBackgroundProcess(ctx context.Context, process *BackgroundProcess) error {
	if process == nil {
		return fmt.Errorf("process cannot be nil")
	}
	if process.ID == "" {
		process.ID = generateID()
	}
	if process.CreatedAt.IsZero() {
		process.CreatedAt = time.Now()
	}
	if process.UpdatedAt.IsZero() {
		process.UpdatedAt = time.Now()
	}
	if process.StartedAt.IsZero() {
		process.StartedAt = time.Now()
	}

	_, err := r.DB.ExecContext(ctx, `
		INSERT INTO background_processes (
			id, pid, command, working_dir, worktree_id, project_id, chat_id, user_id,
			status, exit_code, started_at, ended_at, signature, source_type,
			package_type, command_name, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		process.ID, process.PID, process.Command, process.WorkingDir,
		process.WorktreeID, process.ProjectID, process.ChatID, process.UserID,
		process.Status, process.ExitCode, process.StartedAt, process.EndedAt,
		process.Signature, process.SourceType, process.PackageType, process.CommandName,
		process.CreatedAt, process.UpdatedAt,
	)
	if err == nil {
		return nil
	}
	query := `
		INSERT INTO background_processes (
			id, pid, command, working_dir, worktree_id, project_id, chat_id, user_id,
			status, exit_code, started_at, ended_at, signature, source_type,
			package_type, command_name, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	query = r.bindQuery(query)
	_, err = r.DB.ExecContext(ctx, query,
		process.ID, process.PID, process.Command, process.WorkingDir,
		process.WorktreeID, process.ProjectID, process.ChatID, process.UserID,
		process.Status, process.ExitCode, process.StartedAt, process.EndedAt,
		process.Signature, process.SourceType, process.PackageType, process.CommandName,
		process.CreatedAt, process.UpdatedAt,
	)
	return err
}

func (r *Repo) GetBackgroundProcess(ctx context.Context, id string) (*BackgroundProcess, error) {
	if id == "" {
		return nil, fmt.Errorf("process ID cannot be empty")
	}

	query := `
		SELECT id, pid, command, working_dir, worktree_id, project_id, chat_id, user_id,
			status, exit_code, started_at, ended_at, signature, source_type,
			package_type, command_name, created_at, updated_at
		FROM background_processes WHERE id = ?`
	query = r.bindQuery(query)
	row := r.DB.QueryRowContext(ctx, query, id)

	return scanBackgroundProcess(row)
}

func (r *Repo) ListBackgroundProcesses(ctx context.Context, filters BackgroundProcessFilters) ([]*BackgroundProcess, error) {
	query := `
		SELECT id, pid, command, working_dir, worktree_id, project_id, chat_id, user_id,
			status, exit_code, started_at, ended_at, signature, source_type,
			package_type, command_name, created_at, updated_at
		FROM background_processes WHERE 1=1`
	args := []interface{}{}

	if filters.UserID != "" {
		query += " AND user_id = ?"
		args = append(args, filters.UserID)
	}
	if filters.WorktreeID != nil {
		query += " AND worktree_id = ?"
		args = append(args, *filters.WorktreeID)
	}
	if filters.ProjectID != nil {
		query += " AND project_id = ?"
		args = append(args, *filters.ProjectID)
	}
	if filters.ChatID != nil {
		query += " AND chat_id = ?"
		args = append(args, *filters.ChatID)
	}
	if filters.Status != nil {
		query += " AND status = ?"
		args = append(args, *filters.Status)
	}
	if filters.SourceType != nil {
		query += " AND source_type = ?"
		args = append(args, string(*filters.SourceType))
	}

	query += " ORDER BY started_at DESC"

	if filters.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filters.Limit)
	}
	if filters.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filters.Offset)
	}

	query = r.bindQuery(query)

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var processes []*BackgroundProcess
	for rows.Next() {
		process, err := scanBackgroundProcessFromRows(rows)
		if err != nil {
			return nil, err
		}
		processes = append(processes, process)
	}

	return processes, rows.Err()
}

func (r *Repo) UpdateBackgroundProcessStatus(ctx context.Context, id string, status BackgroundProcessStatus, exitCode *int, endedAt *time.Time) error {
	if id == "" {
		return fmt.Errorf("process ID cannot be empty")
	}

	query := `
		UPDATE background_processes
		SET status = ?, exit_code = ?, ended_at = ?, updated_at = ?
		WHERE id = ?`
	query = r.bindQuery(query)
	_, err := r.DB.ExecContext(ctx, query,
		status, exitCode, endedAt, time.Now(), id,
	)
	return err
}

func (r *Repo) UpdateBackgroundProcessPID(ctx context.Context, id string, pid int, signature string) error {
	if id == "" {
		return fmt.Errorf("process ID cannot be empty")
	}

	query := `
		UPDATE background_processes
		SET pid = ?, signature = ?, updated_at = ?
		WHERE id = ?`
	query = r.bindQuery(query)
	_, err := r.DB.ExecContext(ctx, query,
		pid, signature, time.Now(), id,
	)
	return err
}

func (r *Repo) GetRunningBackgroundProcesses(ctx context.Context) ([]*BackgroundProcess, error) {
	rows, err := r.DB.QueryContext(ctx, `
		SELECT id, pid, command, working_dir, worktree_id, project_id, chat_id, user_id,
			status, exit_code, started_at, ended_at, signature, source_type,
			package_type, command_name, created_at, updated_at
		FROM background_processes
		WHERE status = ?`, BgProcessStatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var processes []*BackgroundProcess
	for rows.Next() {
		process, err := scanBackgroundProcessFromRows(rows)
		if err != nil {
			return nil, err
		}
		processes = append(processes, process)
	}

	return processes, rows.Err()
}

func (r *Repo) MarkStaleProcesses(ctx context.Context, processIDs []string) error {
	if len(processIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(processIDs))
	for i := range processIDs {
		placeholders[i] = "?"
	}

	args := make([]interface{}, len(processIDs)+3)
	args[0] = BgProcessStatusStale
	args[1] = time.Now()
	args[2] = time.Now()
	for i, id := range processIDs {
		args[i+3] = id
	}

	query := fmt.Sprintf(`
		UPDATE background_processes
		SET status = ?, ended_at = ?, updated_at = ?
		WHERE id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	query = r.bindQuery(query)

	_, err := r.DB.ExecContext(ctx, query, args...)
	return err
}

func (r *Repo) CleanupOldBackgroundProcesses(ctx context.Context, olderThan time.Time) (int64, error) {
	query := `
		DELETE FROM background_processes
		WHERE status IN (?, ?, ?, ?, ?)
			AND ended_at < ?`
	query = r.bindQuery(query)
	result, err := r.DB.ExecContext(ctx, query,
		BgProcessStatusCompleted,
		BgProcessStatusFailed,
		BgProcessStatusKilled,
		BgProcessStatusKilledExternally,
		BgProcessStatusStale,
		olderThan,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ==================== Background Process Output ====================

func (r *Repo) CreateBackgroundProcessOutputBatch(ctx context.Context, lines []BackgroundProcessOutputLine) error {
	if len(lines) == 0 {
		return nil
	}

	// Build a single multi-row INSERT for efficiency.
	// Each row has 4 value placeholders: (process_id, seq, stream, line)
	var b strings.Builder
	b.WriteString("INSERT INTO background_process_output (process_id, seq, stream, line) VALUES ")
	args := make([]interface{}, 0, len(lines)*4)
	for i, line := range lines {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(?, ?, ?, ?)")
		args = append(args, line.ProcessID, line.Seq, line.Stream, line.Line)
	}

	query := r.bindQuery(b.String())
	_, err := r.DB.ExecContext(ctx, query, args...)
	return err
}

func (r *Repo) GetBackgroundProcessOutput(ctx context.Context, processID string, afterSeq int64, limit int) ([]BackgroundProcessOutputLine, error) {
	if processID == "" {
		return nil, fmt.Errorf("process ID cannot be empty")
	}
	if limit <= 0 {
		limit = 10000
	}

	query := `
		SELECT id, process_id, seq, stream, line, created_at
		FROM background_process_output
		WHERE process_id = ? AND seq > ?
		ORDER BY seq ASC
		LIMIT ?`
	query = r.bindQuery(query)
	rows, err := r.DB.QueryContext(ctx, query, processID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []BackgroundProcessOutputLine
	for rows.Next() {
		var line BackgroundProcessOutputLine
		if err := rows.Scan(&line.ID, &line.ProcessID, &line.Seq, &line.Stream, &line.Line, &line.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, line)
	}
	return result, rows.Err()
}

// scanBackgroundProcess scans a single row into a BackgroundProcess
func scanBackgroundProcess(row *sql.Row) (*BackgroundProcess, error) {
	var p BackgroundProcess
	var pid, exitCode sql.NullInt64
	var worktreeID, projectID, chatID, signature, packageType, commandName sql.NullString
	var endedAt sql.NullTime

	err := row.Scan(
		&p.ID, &pid, &p.Command, &p.WorkingDir,
		&worktreeID, &projectID, &chatID, &p.UserID,
		&p.Status, &exitCode, &p.StartedAt, &endedAt,
		&signature, &p.SourceType, &packageType, &commandName,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("process not found")
		}
		return nil, err
	}

	if pid.Valid {
		pidInt := int(pid.Int64)
		p.PID = &pidInt
	}
	if exitCode.Valid {
		exitCodeInt := int(exitCode.Int64)
		p.ExitCode = &exitCodeInt
	}
	if worktreeID.Valid {
		p.WorktreeID = &worktreeID.String
	}
	if projectID.Valid {
		p.ProjectID = &projectID.String
	}
	if chatID.Valid {
		p.ChatID = &chatID.String
	}
	if signature.Valid {
		p.Signature = &signature.String
	}
	if packageType.Valid {
		p.PackageType = &packageType.String
	}
	if commandName.Valid {
		p.CommandName = &commandName.String
	}
	if endedAt.Valid {
		p.EndedAt = &endedAt.Time
	}

	return &p, nil
}

// scanBackgroundProcessFromRows scans a row from sql.Rows into a BackgroundProcess
func scanBackgroundProcessFromRows(rows *sql.Rows) (*BackgroundProcess, error) {
	var p BackgroundProcess
	var pid, exitCode sql.NullInt64
	var worktreeID, projectID, chatID, signature, packageType, commandName sql.NullString
	var endedAt sql.NullTime

	err := rows.Scan(
		&p.ID, &pid, &p.Command, &p.WorkingDir,
		&worktreeID, &projectID, &chatID, &p.UserID,
		&p.Status, &exitCode, &p.StartedAt, &endedAt,
		&signature, &p.SourceType, &packageType, &commandName,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if pid.Valid {
		pidInt := int(pid.Int64)
		p.PID = &pidInt
	}
	if exitCode.Valid {
		exitCodeInt := int(exitCode.Int64)
		p.ExitCode = &exitCodeInt
	}
	if worktreeID.Valid {
		p.WorktreeID = &worktreeID.String
	}
	if projectID.Valid {
		p.ProjectID = &projectID.String
	}
	if chatID.Valid {
		p.ChatID = &chatID.String
	}
	if signature.Valid {
		p.Signature = &signature.String
	}
	if packageType.Valid {
		p.PackageType = &packageType.String
	}
	if commandName.Valid {
		p.CommandName = &commandName.String
	}
	if endedAt.Valid {
		p.EndedAt = &endedAt.Time
	}

	return &p, nil
}

// ==================== Attachments ====================

func (r *Repo) CreateAttachment(ctx context.Context, attachment *Attachment) error {
	return r.attachments.CreateAttachment(ctx, attachment)
}

func (r *Repo) GetAttachment(ctx context.Context, id string) (*Attachment, error) {
	return r.attachments.GetAttachment(ctx, id)
}

func (r *Repo) GetAttachmentsByIDs(ctx context.Context, ids []string) ([]*Attachment, error) {
	return r.attachments.GetAttachmentsByIDs(ctx, ids)
}

func (r *Repo) DeleteAttachment(ctx context.Context, id string) error {
	return r.attachments.DeleteAttachment(ctx, id)
}

// ==================== Context Usage ====================

// GetContextUsage returns context usage info for the compaction indicator.
//
// CONTEXT SEQUENCE RULE:
// Token count is calculated for the current context_sequence only.
// When compaction occurs, context_sequence increments and the token count
// resets (only counting messages in the new context).
//
// FORK INHERITANCE:
// For forked threads, we include parent's token count at the SAME context_sequence.
// This works because forked threads inherit their starting context_sequence from
// the parent. When either compacts, the context_sequence increments and the
// inheritance chain breaks naturally.
//
// GetThreadTokenCount returns the cumulative token count for a thread.
// This handles fork inheritance automatically by walking up the fork chain.
//
// Parameters:
// - thread: the thread ID
// - maxOrdinal: optional - if set, returns tokens at that ordinal (for fork points)
//
//	if nil, returns current tokens (most recent message with data)
//
// Returns 0 if no token data exists (caller should estimate if needed).
func (r *Repo) GetThreadTokenCount(ctx context.Context, thread string, maxOrdinal *int64) (int64, error) {
	if thread == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	// Get the Thread record to find conversation_id and fork metadata
	threadRecord, _, err := r.GetThreadWithParent(ctx, thread)
	if err != nil {
		// Thread not found - try querying messages directly using thread as both chat_id and thread
		// This handles legacy data where Thread records may not exist
		logging.Debug("[GetThreadTokenCount] Thread record not found, trying direct query",
			"thread", thread,
			"error", err)
		return r.getThreadTokenCountDirect(ctx, thread, thread, maxOrdinal)
	}

	// Get current context sequence for the thread
	contextSeq, err := r.GetMaxContextSequenceInThread(ctx, thread)
	if err != nil {
		logging.Warn("[GetThreadTokenCount] Error getting context sequence",
			"thread", thread,
			"error", err)
		contextSeq = 0
	}

	// Query for token count at or before maxOrdinal
	var maxOrdinalSQL sql.NullInt64
	if maxOrdinal != nil {
		maxOrdinalSQL = sql.NullInt64{Int64: *maxOrdinal, Valid: true}
	}

	var localTokens int64
	localTokens, err = r.tokenCounts.GetThreadTokenCountAtOrdinal(ctx, thread, int64(contextSeq), maxOrdinalSQL)
	if err != nil {
		logging.Warn("[GetThreadTokenCount] Error querying token count",
			"thread", thread,
			"contextSequence", contextSeq,
			"maxOrdinal", maxOrdinal,
			"error", err)
		localTokens = 0
	}

	// If we have local token data, return it
	if localTokens > 0 {
		logging.Debug("[GetThreadTokenCount] Found local token data",
			"thread", thread,
			"tokens", localTokens)
		return localTokens, nil
	}

	// No local token data - check for fork inheritance
	if threadRecord.ParentThreadID == nil || threadRecord.ForkAtOrdinal == nil {
		// No fork metadata, return 0
		logging.Debug("[GetThreadTokenCount] No local tokens and no fork metadata",
			"thread", thread)
		return 0, nil
	}

	parentThreadID := *threadRecord.ParentThreadID
	forkAtOrdinal := *threadRecord.ForkAtOrdinal

	// Guard against self-referential forks
	if parentThreadID == thread {
		logging.Warn("[GetThreadTokenCount] Skipping self-referential fork",
			"thread", thread)
		return 0, nil
	}

	// Recursively get parent's token count at the fork point
	logging.Debug("[GetThreadTokenCount] Inheriting tokens from parent",
		"thread", thread,
		"parentThread", parentThreadID,
		"forkAtOrdinal", forkAtOrdinal)

	return r.GetThreadTokenCount(ctx, parentThreadID, &forkAtOrdinal)
}

// getThreadTokenCountDirect queries token count directly using chat_id and thread.
// Used as fallback when Thread record doesn't exist.
func (r *Repo) getThreadTokenCountDirect(ctx context.Context, chatID, thread string, maxOrdinal *int64) (int64, error) {
	_ = chatID // kept for API compatibility

	// Get context sequence
	contextSeq, err := r.GetMaxContextSequenceInThread(ctx, thread)
	if err != nil {
		contextSeq = 0
	}

	// Build query
	var query string
	var args []interface{}
	if maxOrdinal != nil {
		query = `
			SELECT CAST(COALESCE(
				(
					SELECT COALESCE(token_count, 0)
					FROM messages m
					JOIN context_windows cw ON cw.id = m.context_window_id
					WHERE cw.thread_id = ? AND cw.sequence = ?
					  AND token_count IS NOT NULL
					  AND ordinal <= ?
					ORDER BY ordinal DESC
					LIMIT 1
				), 0) AS INTEGER)`
		args = []interface{}{thread, contextSeq, *maxOrdinal}
	} else {
		query = `
			SELECT CAST(COALESCE(
				(
					SELECT COALESCE(token_count, 0)
					FROM messages m
					JOIN context_windows cw ON cw.id = m.context_window_id
					WHERE cw.thread_id = ? AND cw.sequence = ?
					  AND token_count IS NOT NULL
					ORDER BY ordinal DESC
					LIMIT 1
				), 0) AS INTEGER)`
		args = []interface{}{thread, contextSeq}
	}
	query = r.bindQuery(query)

	var tokens int64
	err = r.DB.QueryRowContext(ctx, query, args...).Scan(&tokens)
	if err != nil {
		return 0, nil
	}
	return tokens, nil
}

// GetContextUsage returns context usage info for the compaction indicator.
// Uses the unified GetThreadTokenCount which handles fork inheritance automatically.
func (r *Repo) GetContextUsage(ctx context.Context, chatID, thread string) (*ContextUsage, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	if thread == "" {
		return nil, fmt.Errorf("thread cannot be empty")
	}

	// Get token count using unified function (handles fork inheritance)
	tokens, err := r.GetThreadTokenCount(ctx, thread, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get thread token count: %w", err)
	}

	// Get current context sequence
	contextSeq, err := r.GetMaxContextSequenceInThread(ctx, thread)
	if err != nil {
		return nil, fmt.Errorf("failed to get max context sequence: %w", err)
	}

	return &ContextUsage{
		ThreadTokenCount:    tokens,
		CompactionThreshold: int64(r.resolveThreadCompactionThreshold(ctx, thread, nil)),
		CurrentContextSeq:   int64(contextSeq),
	}, nil
}

// resolveThreadCompactionThreshold returns the compaction threshold for the
// model that produced the thread's current token count, mirroring the fork
// inheritance walk in GetThreadTokenCount so the indicator's denominator tracks
// the same model the trigger evaluates. Falls back to the global default when
// no model-bearing message is found.
func (r *Repo) resolveThreadCompactionThreshold(ctx context.Context, thread string, maxOrdinal *int64) int {
	if thread == "" {
		return models.GlobalDefaultCompactionThreshold
	}

	contextSeq, err := r.GetMaxContextSequenceInThread(ctx, thread)
	if err != nil {
		contextSeq = 0
	}

	msg, err := r.GetLatestMessageWithTokensInThread(ctx, thread, contextSeq)
	if err == nil && msg != nil && msg.TokenCount != nil && (maxOrdinal == nil || msg.Ordinal <= *maxOrdinal) {
		if msg.Model != nil {
			return models.CompactionThresholdForModel(*msg.Model)
		}
		return models.GlobalDefaultCompactionThreshold
	}

	// No local token-bearing message — follow fork inheritance like GetThreadTokenCount.
	threadRecord, _, err := r.GetThreadWithParent(ctx, thread)
	if err != nil || threadRecord.ParentThreadID == nil || threadRecord.ForkAtOrdinal == nil {
		return models.GlobalDefaultCompactionThreshold
	}
	parentThreadID := *threadRecord.ParentThreadID
	if parentThreadID == thread {
		return models.GlobalDefaultCompactionThreshold
	}
	forkAtOrdinal := *threadRecord.ForkAtOrdinal
	return r.resolveThreadCompactionThreshold(ctx, parentThreadID, &forkAtOrdinal)
}

// ==================== Workflows ====================

func (r *Repo) CreateWorkflow(ctx context.Context, workflow *Workflow) error {
	if workflow == nil {
		return fmt.Errorf("workflow cannot be nil")
	}

	return r.RunTx(ctx, func(txCtx context.Context) error {
		if err := r.workflows.CreateWorkflow(txCtx, workflow); err != nil {
			return err
		}
		if workflow.ChatID != "" {
			return r.emitChatActivityIfChanged(txCtx, workflow.ChatID)
		}
		return nil
	})
}

func (r *Repo) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	return r.workflows.GetWorkflow(ctx, id)
}

func (r *Repo) GetWorkflowByThread(ctx context.Context, chatID, thread string) (*Workflow, error) {
	return r.workflows.GetWorkflowByThread(ctx, chatID, thread)
}

func (r *Repo) ListWorkflowsByChat(ctx context.Context, chatID string) ([]*Workflow, error) {
	return r.workflows.ListWorkflowsByChat(ctx, chatID)
}

func (r *Repo) ListChildWorkflows(ctx context.Context, parentID string) ([]*Workflow, error) {
	return r.workflows.ListChildWorkflows(ctx, parentID)
}

func (r *Repo) ListRootWorkflows(ctx context.Context, chatID string) ([]*Workflow, error) {
	return r.workflows.ListRootWorkflows(ctx, chatID)
}

func (r *Repo) GetRootWorkflowStatusForChats(ctx context.Context, chatIDs []string) (map[string]WorkflowStatus, error) {
	if len(chatIDs) == 0 {
		return make(map[string]WorkflowStatus), nil
	}
	return r.workflows.GetRootWorkflowStatusForChats(ctx, chatIDs)
}

func (r *Repo) CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus WorkflowStatus) (bool, error) {
	var swapped bool
	err := r.RunTx(ctx, func(txCtx context.Context) error {
		ok, err := r.workflows.CompareAndSwapWorkflowStatus(txCtx, id, newStatus, expectedStatus)
		if err != nil {
			return err
		}
		swapped = ok
		if !ok {
			return nil // CAS failed, nothing to emit
		}
		wf, err := r.GetWorkflow(txCtx, id)
		if err != nil {
			return err
		}
		if wf.ChatID == "" {
			return nil
		}
		return r.emitChatActivityIfChanged(txCtx, wf.ChatID)
	})
	return swapped, err
}

func (r *Repo) UpdateWorkflowStatus(ctx context.Context, id string, status WorkflowStatus) error {
	return r.RunTx(ctx, func(txCtx context.Context) error {
		if err := r.workflows.UpdateWorkflowStatus(txCtx, id, status); err != nil {
			return err
		}
		wf, err := r.GetWorkflow(txCtx, id)
		if err != nil {
			return err
		}
		if wf.ChatID == "" {
			return nil // no chat association
		}
		return r.emitChatActivityIfChanged(txCtx, wf.ChatID)
	})
}

// EnsureWorkflowRunning is a lightweight idempotent check: if the workflow
// is not already Running, transition it to Running and emit an activity event.
// Designed to be called from the activity wrapper so that any Temporal activity
// execution automatically flips the chat into the active state.
// Returns quickly with no DB writes when the workflow is already Running.
func (r *Repo) EnsureWorkflowRunning(ctx context.Context, workflowID, chatID string) {
	if workflowID == "" {
		return
	}
	wf, err := r.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return // Workflow not tracked yet (WorkflowStatus "started" hasn't fired)
	}
	if wf.Status == WorkflowStatusRunning {
		return // Fast path — already running, nothing to do
	}
	if wf.Status == WorkflowStatusPaused {
		return // Paused is a deliberate user action — don't override it
	}

	logging.Info("[EnsureWorkflowRunning] Workflow not marked running, correcting",
		"workflowID", workflowID,
		"chatID", chatID,
		"currentStatus", wf.Status,
	)
	if err := r.UpdateWorkflowStatus(ctx, workflowID, WorkflowStatusRunning); err != nil {
		logging.Warn("[EnsureWorkflowRunning] Failed to update workflow status",
			"workflowID", workflowID,
			"error", err,
		)
	}
}

// UpdateWorkflowName updates the workflow name. Only allowed when status is 'pending'.
// Returns error if workflow doesn't exist or status is not pending.
func (r *Repo) UpdateWorkflowName(ctx context.Context, id string, workflowName string) error {
	return r.workflows.UpdateWorkflowName(ctx, id, workflowName)
}

// CompleteChildWorkflows marks all child workflow records owned by a parent as completed.
// Called when a workflow reaches a terminal state to cascade to all children
// (spawn children, thread records, etc.) that are still running.
func (r *Repo) CompleteChildWorkflows(ctx context.Context, parentWorkflowID string) error {
	return r.workflows.CompleteChildWorkflows(ctx, parentWorkflowID)
}

func (r *Repo) PauseRunningWorkflowsByChat(ctx context.Context, chatID string) error {
	return r.RunTx(ctx, func(txCtx context.Context) error {
		if err := r.workflows.PauseRunningWorkflowsByChat(txCtx, chatID); err != nil {
			return err
		}
		return r.emitChatActivityIfChanged(txCtx, chatID)
	})
}

func (r *Repo) ResumeWorkflowsByChat(ctx context.Context, chatID string) error {
	return r.RunTx(ctx, func(txCtx context.Context) error {
		if err := r.workflows.ResumeWorkflowsByChat(txCtx, chatID); err != nil {
			return err
		}
		return r.emitChatActivityIfChanged(txCtx, chatID)
	})
}

func (r *Repo) DeleteWorkflow(ctx context.Context, id string) error {
	return r.workflows.DeleteWorkflow(ctx, id)
}

func (r *Repo) DeleteWorkflowsByChat(ctx context.Context, chatID string) error {
	return r.workflows.DeleteWorkflowsByChat(ctx, chatID)
}

// ==================== Step Executions ====================

func (r *Repo) CreateStepExecution(ctx context.Context, exec *StepExecution) error {
	if exec == nil {
		return fmt.Errorf("step execution cannot be nil")
	}

	return r.workflows.CreateStepExecution(ctx, exec)
}

func (r *Repo) GetStepExecution(ctx context.Context, id string) (*StepExecution, error) {
	if id == "" {
		return nil, fmt.Errorf("step execution ID cannot be empty")
	}

	return r.workflows.GetStepExecution(ctx, id)
}

func (r *Repo) GetStepExecutionsByWorkflow(ctx context.Context, workflowID string) ([]*StepExecution, error) {
	return r.workflows.GetStepExecutionsByWorkflow(ctx, workflowID)
}

func (r *Repo) GetStepExecutionsByStep(ctx context.Context, workflowID, stepID string) ([]*StepExecution, error) {
	return r.workflows.GetStepExecutionsByStep(ctx, workflowID, stepID)
}

func (r *Repo) DeleteStepExecutionsByWorkflow(ctx context.Context, workflowID string) error {
	return r.workflows.DeleteStepExecutionsByWorkflow(ctx, workflowID)
}

func (r *Repo) ListWorkflowsByStatus(ctx context.Context, status WorkflowStatus) ([]*Workflow, error) {
	return r.workflows.ListWorkflowsByStatus(ctx, status)
}

func (r *Repo) ListRootWorkflowsByStatus(ctx context.Context, status WorkflowStatus) ([]*Workflow, error) {
	return r.workflows.ListRootWorkflowsByStatus(ctx, status)
}

// ==================== Workflow Checkpoints ====================
// Position truth for resume-at-position: last top-level node entered (and
// loop iteration for loop nodes) per workflow ID.

func (r *Repo) UpsertWorkflowCheckpoint(ctx context.Context, checkpoint *WorkflowCheckpoint) error {
	if checkpoint == nil {
		return fmt.Errorf("checkpoint cannot be nil")
	}
	return r.workflows.UpsertWorkflowCheckpoint(ctx, checkpoint)
}

// GetWorkflowCheckpoint returns the recorded position for a workflow ID, or
// (nil, nil) when no checkpoint exists.
func (r *Repo) GetWorkflowCheckpoint(ctx context.Context, workflowID string) (*WorkflowCheckpoint, error) {
	return r.workflows.GetWorkflowCheckpoint(ctx, workflowID)
}

func (r *Repo) DeleteWorkflowCheckpoint(ctx context.Context, workflowID string) error {
	return r.workflows.DeleteWorkflowCheckpoint(ctx, workflowID)
}

// UpdateWorkflowWorkerStarted records when a worker was started for a workflow.
// This is used by the reconciler to skip stuck-check for recently started workers.
func (r *Repo) UpdateWorkflowWorkerStarted(ctx context.Context, workflowID string) error {
	return r.workflows.UpdateWorkflowWorkerStarted(ctx, workflowID)
}

// UpdateWorkflowWorkerStopped records when a worker was stopped for a workflow.
// This tracks the worker lifecycle for pause/resume scenarios.
func (r *Repo) UpdateWorkflowWorkerStopped(ctx context.Context, workflowID string) error {
	return r.workflows.UpdateWorkflowWorkerStopped(ctx, workflowID)
}

// ==================== Threads ====================
// First-class entity for thread hierarchy and fork relationships

func (r *Repo) CreateThread(ctx context.Context, thread *Thread) (*Thread, error) {
	if thread == nil {
		return nil, fmt.Errorf("thread cannot be nil")
	}
	if thread.ID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	if thread.ConversationID == "" {
		return nil, fmt.Errorf("conversation ID cannot be empty")
	}
	return r.threads.CreateThread(ctx, thread)
}

func (r *Repo) GetThread(ctx context.Context, id string) (*Thread, error) {
	if id == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.threads.GetThread(ctx, id)
}

func (r *Repo) GetThreadByWorkflow(ctx context.Context, workflowID string) (*Thread, error) {
	if workflowID == "" {
		return nil, fmt.Errorf("workflow ID cannot be empty")
	}
	return r.threads.GetThreadByWorkflow(ctx, workflowID)
}

func (r *Repo) GetRootThread(ctx context.Context, conversationID string) (*Thread, error) {
	if conversationID == "" {
		return nil, fmt.Errorf("conversation ID cannot be empty")
	}
	return r.threads.GetRootThread(ctx, conversationID)
}

func (r *Repo) GetThreadWithParent(ctx context.Context, id string) (*Thread, *string, error) {
	if id == "" {
		return nil, nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.threads.GetThreadWithParent(ctx, id)
}

func (r *Repo) ListThreadsByConversation(ctx context.Context, conversationID string) ([]*Thread, error) {
	if conversationID == "" {
		return nil, fmt.Errorf("conversation ID cannot be empty")
	}
	return r.threads.ListThreadsByConversation(ctx, conversationID)
}

func (r *Repo) ListChildThreads(ctx context.Context, parentThreadID string) ([]*Thread, error) {
	if parentThreadID == "" {
		return nil, fmt.Errorf("parent thread ID cannot be empty")
	}
	return r.threads.ListChildThreads(ctx, parentThreadID)
}

func (r *Repo) UpdateThreadWorkflow(ctx context.Context, threadID, workflowID string) (*Thread, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.threads.UpdateThreadWorkflow(ctx, threadID, workflowID)
}

func (r *Repo) UpdateThreadForkPoint(ctx context.Context, threadID string, forkAtOrdinal *int64, forkAtContextWindowID *string) (*Thread, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.threads.UpdateThreadForkPoint(ctx, threadID, forkAtOrdinal, forkAtContextWindowID)
}

func (r *Repo) DeleteThread(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("thread ID cannot be empty")
	}
	return r.threads.DeleteThread(ctx, id)
}

func (r *Repo) DeleteThreadsByConversation(ctx context.Context, conversationID string) error {
	if conversationID == "" {
		return fmt.Errorf("conversation ID cannot be empty")
	}
	return r.threads.DeleteThreadsByConversation(ctx, conversationID)
}

func (r *Repo) CountThreadsInConversation(ctx context.Context, conversationID string) (int64, error) {
	if conversationID == "" {
		return 0, fmt.Errorf("conversation ID cannot be empty")
	}
	return r.threads.CountThreadsInConversation(ctx, conversationID)
}

// ==================== Context Windows ====================
// Atomic unit for what gets sent to the LLM

func (r *Repo) CreateContextWindow(ctx context.Context, cw *ContextWindow) (*ContextWindow, error) {
	if cw == nil {
		return nil, fmt.Errorf("context window cannot be nil")
	}
	if cw.ID == "" {
		return nil, fmt.Errorf("context window ID cannot be empty")
	}
	if cw.ThreadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.contextWindows.CreateContextWindow(ctx, cw)
}

func (r *Repo) GetContextWindow(ctx context.Context, id string) (*ContextWindow, error) {
	if id == "" {
		return nil, fmt.Errorf("context window ID cannot be empty")
	}
	return r.contextWindows.GetContextWindow(ctx, id)
}

func (r *Repo) GetLatestContextWindow(ctx context.Context, threadID string) (*ContextWindow, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.contextWindows.GetLatestContextWindow(ctx, threadID)
}

func (r *Repo) GetContextWindowBySequence(ctx context.Context, threadID string, sequence int) (*ContextWindow, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.contextWindows.GetContextWindowBySequence(ctx, threadID, sequence)
}

func (r *Repo) GetContextWindowWithThread(ctx context.Context, id string) (*ContextWindow, string, *string, *int64, error) {
	if id == "" {
		return nil, "", nil, nil, fmt.Errorf("context window ID cannot be empty")
	}
	return r.contextWindows.GetContextWindowWithThread(ctx, id)
}

func (r *Repo) ListContextWindowsByThread(ctx context.Context, threadID string) ([]*ContextWindow, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.contextWindows.ListContextWindowsByThread(ctx, threadID)
}

func (r *Repo) GetMaxSequenceForThread(ctx context.Context, threadID string) (int, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}
	return r.contextWindows.GetMaxSequenceForThread(ctx, threadID)
}

func (r *Repo) SetCompactionSummaryMessage(ctx context.Context, cwID, messageID string) (*ContextWindow, error) {
	if cwID == "" {
		return nil, fmt.Errorf("context window ID cannot be empty")
	}
	return r.contextWindows.SetCompactionSummaryMessage(ctx, cwID, messageID)
}

func (r *Repo) DeleteContextWindow(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("context window ID cannot be empty")
	}
	return r.contextWindows.DeleteContextWindow(ctx, id)
}

func (r *Repo) DeleteContextWindowsByThread(ctx context.Context, threadID string) error {
	if threadID == "" {
		return fmt.Errorf("thread ID cannot be empty")
	}
	return r.contextWindows.DeleteContextWindowsByThread(ctx, threadID)
}

// ==================== Workflow Drafts ==
// User-owned workflows available across all projects

func (r *Repo) CreateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error {
	if draft == nil {
		return fmt.Errorf("draft cannot be nil")
	}
	return r.workflowCatalog.CreateWorkflowDraft(ctx, draft)
}

func (r *Repo) UpsertWorkflowDraft(ctx context.Context, draft *WorkflowDraft) (*WorkflowDraft, error) {
	if draft == nil {
		return nil, fmt.Errorf("draft cannot be nil")
	}
	return r.workflowCatalog.UpsertWorkflowDraft(ctx, draft)
}

func (r *Repo) GetWorkflowDraft(ctx context.Context, id string) (*WorkflowDraft, error) {
	row, err := r.workflowCatalog.GetWorkflowDraft(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("workflow draft not found: %s", id)
		}
		return nil, err
	}
	return row, nil
}

func (r *Repo) GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error) {
	return r.workflowCatalog.GetWorkflowDraftBySlug(ctx, userID, slug)
}

func (r *Repo) GetWorkflowDraftByName(ctx context.Context, userID, name string) (*WorkflowDraft, error) {
	return r.workflowCatalog.GetWorkflowDraftByName(ctx, userID, name)
}

func (r *Repo) GetWorkflowDraftByChatID(ctx context.Context, chatID string) (*WorkflowDraft, error) {
	return r.workflowCatalog.GetWorkflowDraftByChatID(ctx, chatID)
}

func (r *Repo) GetWorkflowDraftBySourcePath(ctx context.Context, userID, sourcePath string) (*WorkflowDraft, error) {
	return r.workflowCatalog.GetWorkflowDraftBySourcePath(ctx, userID, sourcePath)
}

func (r *Repo) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error) {
	return r.workflowCatalog.GetUsableWorkflowBySlug(ctx, userID, slug)
}

func (r *Repo) ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*WorkflowDraft, error) {
	return r.workflowCatalog.ListWorkflowDraftsByUser(ctx, userID)
}

func (r *Repo) ListUsableWorkflowsByUser(ctx context.Context, userID string) ([]*WorkflowDraft, error) {
	return r.workflowCatalog.ListUsableWorkflowsByUser(ctx, userID)
}

func (r *Repo) UpdateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error {
	if draft == nil {
		return fmt.Errorf("draft cannot be nil")
	}

	return r.workflowCatalog.UpdateWorkflowDraft(ctx, draft)
}

func (r *Repo) UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, isValid bool, validationErrors *string) error {
	return r.workflowCatalog.UpdateWorkflowDraftDefinition(ctx, id, name, slug, definition, isValid, validationErrors)
}

func (r *Repo) SetWorkflowDraftHidden(ctx context.Context, id string, isHidden bool) (*WorkflowDraft, error) {
	return r.workflowCatalog.SetWorkflowDraftHidden(ctx, id, isHidden)
}

func (r *Repo) DeleteWorkflowDraft(ctx context.Context, id string) error {
	return r.workflowCatalog.DeleteWorkflowDraft(ctx, id)
}

func (r *Repo) DeleteWorkflowDraftBySlug(ctx context.Context, userID, slug string) error {
	return r.workflowCatalog.DeleteWorkflowDraftBySlug(ctx, userID, slug)
}

func (r *Repo) WorkflowSlugExists(ctx context.Context, userID, slug string) (bool, error) {
	return r.workflowCatalog.WorkflowSlugExists(ctx, userID, slug)
}

func (r *Repo) CountWorkflowDraftsByUser(ctx context.Context, userID string) (int64, error) {
	return r.workflowCatalog.CountWorkflowDraftsByUser(ctx, userID)
}

func (r *Repo) AssociateChatWithDraft(ctx context.Context, draftID string, chatID string) (*WorkflowDraft, error) {
	return r.workflowCatalog.AssociateChatWithDraft(ctx, draftID, chatID)
}

func (r *Repo) UpdateWorkflowForkedFrom(ctx context.Context, draftID string, forkedFrom string) (*WorkflowDraft, error) {
	return r.workflowCatalog.UpdateWorkflowForkedFrom(ctx, draftID, forkedFrom)
}

// ==================== Preset Repository ====================

func (r *Repo) CreatePreset(ctx context.Context, preset *Preset) (*Preset, error) {
	return r.workflowCatalog.CreatePreset(ctx, preset)
}

func (r *Repo) UpsertPreset(ctx context.Context, preset *Preset) (*Preset, error) {
	return r.workflowCatalog.UpsertPreset(ctx, preset)
}

func (r *Repo) GetPreset(ctx context.Context, id string) (*Preset, error) {
	return r.workflowCatalog.GetPreset(ctx, id)
}

func (r *Repo) GetPresetBySlug(ctx context.Context, userID, slug string) (*Preset, error) {
	return r.workflowCatalog.GetPresetBySlug(ctx, userID, slug)
}

func (r *Repo) GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*Preset, error) {
	return r.workflowCatalog.GetPresetBySlugAndProject(ctx, userID, slug, projectID)
}

func (r *Repo) ListUserPresets(ctx context.Context, userID string) ([]*Preset, error) {
	return r.workflowCatalog.ListUserPresets(ctx, userID)
}

func (r *Repo) ListUserPresetsGlobal(ctx context.Context, userID string) ([]*Preset, error) {
	return r.workflowCatalog.ListUserPresetsGlobal(ctx, userID)
}

func (r *Repo) ListUserPresetsByProject(ctx context.Context, userID, projectID string) ([]*Preset, error) {
	return r.workflowCatalog.ListUserPresetsByProject(ctx, userID, projectID)
}

func (r *Repo) ListPresetsByTag(ctx context.Context, userID, tag, projectID string) ([]*Preset, error) {
	return r.workflowCatalog.ListPresetsByTag(ctx, userID, tag, projectID)
}

func (r *Repo) UpdatePreset(ctx context.Context, preset *Preset) (*Preset, error) {
	return r.workflowCatalog.UpdatePreset(ctx, preset)
}

func (r *Repo) DeletePreset(ctx context.Context, id string) error {
	return r.workflowCatalog.DeletePreset(ctx, id)
}

func (r *Repo) DeletePresetBySlug(ctx context.Context, userID, slug string) error {
	return r.workflowCatalog.DeletePresetBySlug(ctx, userID, slug)
}

func (r *Repo) DeletePresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) error {
	return r.workflowCatalog.DeletePresetBySlugAndProject(ctx, userID, slug, projectID)
}

// ==================== Workflow Scenario Repository ====================

func (r *Repo) CreateWorkflowScenario(ctx context.Context, scenario *WorkflowScenario) error {
	if scenario == nil {
		return fmt.Errorf("scenario cannot be nil")
	}
	return r.workflowCatalog.CreateWorkflowScenario(ctx, scenario)
}

func (r *Repo) GetWorkflowScenario(ctx context.Context, id string) (*WorkflowScenario, error) {
	return r.workflowCatalog.GetWorkflowScenario(ctx, id)
}

func (r *Repo) GetWorkflowScenarioByName(ctx context.Context, workflowDraftID string, name string) (*WorkflowScenario, error) {
	return r.workflowCatalog.GetWorkflowScenarioByName(ctx, workflowDraftID, name)
}

func (r *Repo) ListWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) ([]*WorkflowScenario, error) {
	return r.workflowCatalog.ListWorkflowScenariosByDraft(ctx, workflowDraftID)
}

func (r *Repo) ListWorkflowScenariosByUser(ctx context.Context, userID string) ([]*WorkflowScenario, error) {
	return r.workflowCatalog.ListWorkflowScenariosByUser(ctx, userID)
}

func (r *Repo) UpdateWorkflowScenario(ctx context.Context, scenario *WorkflowScenario) error {
	if scenario == nil {
		return fmt.Errorf("scenario cannot be nil")
	}
	return r.workflowCatalog.UpdateWorkflowScenario(ctx, scenario)
}

func (r *Repo) UpdateWorkflowScenarioResult(ctx context.Context, id string, status string, result string) error {
	return r.workflowCatalog.UpdateWorkflowScenarioResult(ctx, id, status, result)
}

func (r *Repo) DeleteWorkflowScenario(ctx context.Context, id string) error {
	return r.workflowCatalog.DeleteWorkflowScenario(ctx, id)
}

func (r *Repo) DeleteWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) error {
	return r.workflowCatalog.DeleteWorkflowScenariosByDraft(ctx, workflowDraftID)
}

// ==================== Command Favorites ====================

func (r *Repo) ListCommandFavorites(ctx context.Context, userID, projectID string) ([]string, error) {
	return r.workflows.ListCommandFavorites(ctx, userID, projectID)
}

func (r *Repo) AddCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error {
	return r.workflows.AddCommandFavorite(ctx, userID, projectID, commandKey)
}

func (r *Repo) RemoveCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error {
	return r.workflows.RemoveCommandFavorite(ctx, userID, projectID, commandKey)
}

// ==================== Questions ====================

func (r *Repo) CreateQuestion(ctx context.Context, question *Question) error {
	if question == nil {
		return fmt.Errorf("question cannot be nil")
	}
	if question.ID == "" {
		return fmt.Errorf("question ID cannot be empty")
	}

	query := `INSERT INTO questions (id, chat_id, workflow_id, temporal_workflow_id, thread_id, step_id, loop_node_id, loop_iteration, status, metadata, response_data, created_at, resolved_at, tool_call_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	query = r.bindQuery(query)

	_, err := r.DB.DB(ctx).ExecContext(ctx, query,
		question.ID,
		question.ChatID,
		question.WorkflowID,
		question.TemporalWorkflowID,
		question.ThreadID,
		question.StepID,
		question.LoopNodeID,
		question.LoopIteration,
		question.Status,
		question.Metadata,
		question.ResponseData,
		question.CreatedAt,
		question.ResolvedAt,
		question.ToolCallID,
	)
	if err != nil {
		return fmt.Errorf("failed to create question: %w", err)
	}

	_ = r.emitChatActivityIfChanged(ctx, question.ChatID)
	return nil
}

func (r *Repo) GetQuestionByID(ctx context.Context, id string) (*Question, error) {
	if id == "" {
		return nil, fmt.Errorf("question ID cannot be empty")
	}

	query := `SELECT id, chat_id, workflow_id, temporal_workflow_id, thread_id, step_id, loop_node_id, loop_iteration, status, metadata, response_data, created_at, resolved_at, tool_call_id
		FROM questions WHERE id = ?`
	query = r.bindQuery(query)

	var q Question
	err := r.DB.DB(ctx).QueryRowContext(ctx, query, id).Scan(
		&q.ID, &q.ChatID, &q.WorkflowID, &q.TemporalWorkflowID, &q.ThreadID, &q.StepID,
		&q.LoopNodeID, &q.LoopIteration, &q.Status, &q.Metadata, &q.ResponseData,
		&q.CreatedAt, &q.ResolvedAt, &q.ToolCallID,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get question: %w", err)
	}
	return &q, nil
}

func (r *Repo) GetPendingQuestionByChatID(ctx context.Context, chatID string) (*Question, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	query := `SELECT id, chat_id, workflow_id, temporal_workflow_id, thread_id, step_id, loop_node_id, loop_iteration, status, metadata, response_data, created_at, resolved_at, tool_call_id
		FROM questions WHERE chat_id = ? AND status = 1 ORDER BY created_at DESC LIMIT 1`
	query = r.bindQuery(query)

	var q Question
	err := r.DB.DB(ctx).QueryRowContext(ctx, query, chatID).Scan(
		&q.ID, &q.ChatID, &q.WorkflowID, &q.TemporalWorkflowID, &q.ThreadID, &q.StepID,
		&q.LoopNodeID, &q.LoopIteration, &q.Status, &q.Metadata, &q.ResponseData,
		&q.CreatedAt, &q.ResolvedAt, &q.ToolCallID,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get pending question: %w", err)
	}
	return &q, nil
}

func (r *Repo) GetQuestionsByWorkflowStepIteration(ctx context.Context, workflowID, stepID string, iteration int) ([]*Question, error) {
	query := `SELECT id, chat_id, workflow_id, temporal_workflow_id, thread_id, step_id, loop_node_id, loop_iteration, status, metadata, response_data, created_at, resolved_at, tool_call_id
		FROM questions WHERE workflow_id = ? AND step_id = ? AND loop_iteration = ?`
	query = r.bindQuery(query)

	rows, err := r.DB.DB(ctx).QueryContext(ctx, query, workflowID, stepID, iteration)
	if err != nil {
		return nil, fmt.Errorf("failed to get questions by workflow step iteration: %w", err)
	}
	defer rows.Close()

	var questions []*Question
	for rows.Next() {
		var q Question
		if err := rows.Scan(
			&q.ID, &q.ChatID, &q.WorkflowID, &q.TemporalWorkflowID, &q.ThreadID, &q.StepID,
			&q.LoopNodeID, &q.LoopIteration, &q.Status, &q.Metadata, &q.ResponseData,
			&q.CreatedAt, &q.ResolvedAt, &q.ToolCallID,
		); err != nil {
			return nil, fmt.Errorf("failed to scan question: %w", err)
		}
		questions = append(questions, &q)
	}
	return questions, rows.Err()
}

func (r *Repo) ResolveQuestion(ctx context.Context, id string, responseData *string) error {
	if id == "" {
		return fmt.Errorf("question ID cannot be empty")
	}

	now := time.Now().UTC()
	query := `UPDATE questions SET status = 2, response_data = ?, resolved_at = ? WHERE id = ?`
	query = r.bindQuery(query)

	_, err := r.DB.DB(ctx).ExecContext(ctx, query, responseData, now, id)
	if err != nil {
		return fmt.Errorf("failed to resolve question: %w", err)
	}

	// Emit activity change
	q, err := r.GetQuestionByID(ctx, id)
	if err == nil && q != nil {
		_ = r.emitChatActivityIfChanged(ctx, q.ChatID)
	}

	return nil
}

// ==================== Helper Functions ====================

// generateID generates a new UUID string
func generateID() string {
	return uuid.New().String()
}
