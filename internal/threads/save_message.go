// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// ToolCall is an alias for the canonical message.ToolCall type.
type ToolCall = message.ToolCall

// ToolResult is an alias for the canonical message.ToolResult type.
type ToolResult = message.ToolResult

// ThinkingContent contains extended thinking content from the LLM.
type ThinkingContent struct {
	Content   string `json:"content"`
	Signature string `json:"signature"`
}

// SaveMessageOpts contains options for saving a message to a thread.
type SaveMessageOpts struct {
	// Required fields
	ChatID string
	Thread string
	Role   int32 // MessageRole proto enum value (USER=1, ASSISTANT=2, SYSTEM=3, TOOL=4)

	// Content fields
	Content     string       // Text content
	Attachments []string     // Attachment IDs
	ToolCalls   []ToolCall   // For assistant messages
	ToolResults []ToolResult // For tool messages
	Thinking    *ThinkingContent

	// Token tracking (provided by caller from LLM response)
	// TokenCount represents the context size (how many tokens the LLM saw)
	TokenCount int

	// Display and workflow context
	DisplayStyle int32 // DisplayStyle proto enum value (0=unspecified, 1=info, 2=warning, 3=success, 4=hidden)
	WorkflowID   *string
	StepID       string

	// Context window control
	// When true, creates a new context window with incremented sequence.
	// Used for compaction - the summary message starts fresh context.
	NewContextSequence bool

	// Idempotency (for Temporal activity retries)
	ActivityID    *string
	AttemptNumber int32
}

// SaveMessageResult contains the result of saving a message.
type SaveMessageResult struct {
	MessageID        string
	Ordinal          int64
	ContextWindowID  string
	ThreadTokenCount int
	MessageCount     int
	ToolCalls        []ToolCall   // Pass-through for routing
	ToolResults      []ToolResult // Pass-through for routing
	WasExisting      bool         // True if idempotent return
}

// SaveMessage saves a message to a thread with all content blocks.
// This is the primary method for persisting messages in the threads system.
func (s *Service) SaveMessage(ctx context.Context, opts SaveMessageOpts) (*SaveMessageResult, error) {
	// Validate inputs
	if err := validateSaveMessageOpts(opts); err != nil {
		return nil, err
	}

	// Get context window for thread - thread must already exist
	cw, err := s.GetLatestContextWindow(ctx, opts.Thread)
	if err != nil {
		return nil, fmt.Errorf("thread %s does not exist - must be created before saving messages: %w", opts.Thread, err)
	}

	// Handle new context sequence (for compaction)
	// Creates a new context window with incremented sequence and links to parent CW
	if opts.NewContextSequence {
		parentCWID := cw.ID // Capture parent before reassigning cw
		newSeq := cw.Sequence + 1
		newCWID := contextWindowID(opts.ChatID, opts.Thread, newSeq)
		newCW := &db.ContextWindow{
			ID:                    newCWID,
			ThreadID:              opts.Thread,
			Sequence:              newSeq,
			ParentContextWindowID: &parentCWID, // Link to previous CW for chain traversal
			ForkAtOrdinal:         nil,         // Compaction is not a branch
			CreatedAt:             now(),
		}
		// Try to create - if it exists (idempotent retry), just use it
		if _, err := s.repo.CreateContextWindow(ctx, newCW); err != nil {
			if !strings.Contains(err.Error(), "UNIQUE constraint") {
				return nil, fmt.Errorf("failed to create new context window: %w", err)
			}
			// Already exists - get it
			existingCW, err := s.GetContextWindow(ctx, newCWID)
			if err != nil {
				return nil, fmt.Errorf("failed to get existing context window: %w", err)
			}
			newCW = existingCW
		}
		cw = newCW
	}

	// Check for existing message (idempotency)
	if opts.ActivityID != nil && *opts.ActivityID != "" {
		existing, err := s.checkExistingMessage(ctx, opts, cw)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}

	// Get effective message count
	messageCount, err := s.repo.GetEffectiveMessageCount(ctx, opts.ChatID, opts.Thread)
	if err != nil {
		return nil, fmt.Errorf("failed to count messages: %w", err)
	}

	// Get thread token count from opts (context size from LLM response)
	threadTokenCount := opts.TokenCount
	if threadTokenCount == 0 {
		contextUsage, err := s.repo.GetContextUsage(ctx, opts.ChatID, opts.Thread)
		if err != nil {
			slog.Warn("Failed to get context usage for token count", "error", err)
		} else if contextUsage != nil {
			threadTokenCount = int(contextUsage.ThreadTokenCount)
		}
	}

	// Execute in transaction
	var result SaveMessageResult
	err = s.repo.RunTx(ctx, func(txCtx context.Context) error {
		// Get next ordinal
		ordinal, err := s.repo.GetNextOrdinal(txCtx, opts.Thread)
		if err != nil {
			return fmt.Errorf("failed to get next ordinal: %w", err)
		}

		timestamp := now()
		messageID := uuid.New().String()

		// Create message
		msg := &db.Message{
			ID:              messageID,
			ChatID:          opts.ChatID,
			Ordinal:         ordinal,
			ThreadID:        opts.Thread,
			ContextWindowID: cw.ID,
			Role:            reliantv1.MessageRole(opts.Role),
			WorkflowID:      opts.WorkflowID,
			NodeID:          ptr.StringIfNotEmpty(opts.StepID),
			ActivityID:      opts.ActivityID,
			TokenCount:      ptr.IntIfPositive(opts.TokenCount),
			DisplayStyle:    displayStylePtrIfNonZero(opts.DisplayStyle),
			CreatedAt:       timestamp,
			UpdatedAt:       timestamp,
		}

		if err := s.repo.CreateMessage(txCtx, msg); err != nil {
			return fmt.Errorf("failed to create message: %w", err)
		}

		// Create content blocks based on role
		if err := s.createContentBlocks(txCtx, messageID, opts, timestamp); err != nil {
			return err
		}

		// Emit chat_update for frontend
		if err := s.emitChatUpdate(txCtx, opts, messageID, ordinal, cw.Sequence, threadTokenCount, timestamp); err != nil {
			return err
		}

		result = SaveMessageResult{
			MessageID:        messageID,
			Ordinal:          ordinal,
			ContextWindowID:  cw.ID,
			ThreadTokenCount: threadTokenCount,
			MessageCount:     messageCount + 1,
			ToolCalls:        opts.ToolCalls,
			ToolResults:      opts.ToolResults,
			WasExisting:      false,
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to save message: %w", err)
	}

	slog.Info("[SaveMessage] Created",
		"messageID", result.MessageID,
		"role", opts.Role,
		"ordinal", result.Ordinal,
		"attempt", opts.AttemptNumber)

	return &result, nil
}

// validateSaveMessageOpts validates the save message options.
func validateSaveMessageOpts(opts SaveMessageOpts) error {
	if opts.ChatID == "" {
		return fmt.Errorf("chat_id is required")
	}
	if opts.Thread == "" {
		return fmt.Errorf("thread is required")
	}
	if opts.Role == 0 {
		return fmt.Errorf("role cannot be unspecified")
	}
	switch reliantv1.MessageRole(opts.Role) {
	case reliantv1.MessageRole_MESSAGE_ROLE_USER,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM:
		// valid
	default:
		return fmt.Errorf("invalid role value: %d", opts.Role)
	}

	// Validate display_style if provided
	if opts.DisplayStyle != 0 {
		switch reliantv1.DisplayStyle(opts.DisplayStyle) {
		case reliantv1.DisplayStyle_DISPLAY_STYLE_INFO,
			reliantv1.DisplayStyle_DISPLAY_STYLE_WARNING,
			reliantv1.DisplayStyle_DISPLAY_STYLE_SUCCESS,
			reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN:
			// valid
		default:
			return fmt.Errorf("invalid display_style value: %d", opts.DisplayStyle)
		}
	}

	// Role-specific validation
	switch reliantv1.MessageRole(opts.Role) {
	case reliantv1.MessageRole_MESSAGE_ROLE_USER:
		if opts.Content == "" && len(opts.Attachments) == 0 {
			return fmt.Errorf("content or attachments are required for user messages")
		}
	case reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT:
		// Warn but allow empty assistant messages
		if opts.Content == "" && len(opts.ToolCalls) == 0 {
			slog.Warn("Saving assistant message with no content or tool_calls",
				"chat_id", opts.ChatID,
				"thread", opts.Thread,
			)
		}
	case reliantv1.MessageRole_MESSAGE_ROLE_TOOL:
		if len(opts.ToolResults) == 0 {
			return fmt.Errorf("tool_results is required for tool messages")
		}
	}

	return nil
}

// checkExistingMessage checks for an existing message by activity ID (idempotency).
func (s *Service) checkExistingMessage(ctx context.Context, opts SaveMessageOpts, cw *db.ContextWindow) (*SaveMessageResult, error) {
	var existingMsg *db.Message
	var err error

	if opts.WorkflowID != nil && *opts.WorkflowID != "" {
		existingMsg, err = s.repo.GetMessageByWorkflowAndActivityID(ctx, opts.ChatID, *opts.WorkflowID, *opts.ActivityID)
	} else {
		existingMsg, err = s.repo.GetMessageByActivityID(ctx, opts.ChatID, *opts.ActivityID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing message: %w", err)
	}

	if existingMsg != nil {
		// On first attempt, return existing message
		if opts.AttemptNumber == 1 {
			slog.Info("SaveMessage: Found existing message from same attempt",
				"messageID", existingMsg.ID,
				"attemptNumber", opts.AttemptNumber)

			messageCount, err := s.repo.GetEffectiveMessageCount(ctx, opts.ChatID, opts.Thread)
			if err != nil {
				return nil, fmt.Errorf("failed to get effective message count: %w", err)
			}

			return &SaveMessageResult{
				MessageID:        existingMsg.ID,
				Ordinal:          existingMsg.Ordinal,
				ContextWindowID:  cw.ID,
				ThreadTokenCount: opts.TokenCount,
				MessageCount:     messageCount + 1,
				ToolCalls:        opts.ToolCalls,
				ToolResults:      opts.ToolResults,
				WasExisting:      true,
			}, nil
		}

		// On retry, delete incomplete message and recreate
		slog.Warn("SaveMessage: Deleting incomplete message from failed attempt",
			"messageID", existingMsg.ID,
			"attemptNumber", opts.AttemptNumber)
		if err := s.repo.DeleteMessage(ctx, existingMsg.ID); err != nil {
			return nil, fmt.Errorf("failed to delete incomplete message: %w", err)
		}
	}

	return nil, nil
}

// createContentBlocks creates the appropriate content blocks based on message role.
func (s *Service) createContentBlocks(ctx context.Context, messageID string, opts SaveMessageOpts, timestamp time.Time) error {
	switch reliantv1.MessageRole(opts.Role) {
	case reliantv1.MessageRole_MESSAGE_ROLE_USER:
		return s.createUserContentBlocks(ctx, messageID, opts, timestamp)
	case reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT:
		return s.createAssistantContentBlocks(ctx, messageID, opts, timestamp)
	case reliantv1.MessageRole_MESSAGE_ROLE_TOOL:
		return s.createToolContentBlocks(ctx, messageID, opts, timestamp)
	case reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM:
		return s.createSystemContentBlocks(ctx, messageID, opts, timestamp)
	}
	return nil
}

// createUserContentBlocks creates content blocks for user messages.
func (s *Service) createUserContentBlocks(ctx context.Context, messageID string, opts SaveMessageOpts, timestamp time.Time) error {
	position := 0

	// Create text block if content is provided
	if opts.Content != "" {
		block := &db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: messageID,
			Position:  position,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &opts.Content,
			Version:   ptr.Of(1),
			CreatedAt: timestamp,
			UpdatedAt: timestamp,
		}
		if err := s.repo.CreateContentBlock(ctx, block); err != nil {
			return fmt.Errorf("failed to create text content block: %w", err)
		}
		position++
	}

	// Create attachment blocks
	for _, attachmentID := range opts.Attachments {
		att, err := s.repo.GetAttachment(ctx, attachmentID)
		if err != nil {
			return fmt.Errorf("failed to get attachment metadata for %s: %w", attachmentID, err)
		}
		if att == nil {
			return fmt.Errorf("failed to get attachment metadata for %s: attachment not found", attachmentID)
		}

		var blockType reliantv1.ContentBlockType
		switch att.AttachmentType {
		case string(attachment.TypeImage):
			blockType = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE
		case string(attachment.TypeFileReference):
			blockType = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE
		default:
			return fmt.Errorf("invalid attachment type %q for attachment %s", att.AttachmentType, attachmentID)
		}

		attachmentRef := attachmentID
		block := &db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: messageID,
			Position:  position,
			BlockType: blockType,
			Content:   &attachmentRef,
			Version:   ptr.Of(1),
			CreatedAt: timestamp,
			UpdatedAt: timestamp,
		}
		if err := s.repo.CreateContentBlock(ctx, block); err != nil {
			return fmt.Errorf("failed to create attachment content block: %w", err)
		}
		position++
	}

	return nil
}

// createAssistantContentBlocks creates content blocks for assistant messages.
func (s *Service) createAssistantContentBlocks(ctx context.Context, messageID string, opts SaveMessageOpts, timestamp time.Time) error {
	position := 0

	// Create thinking block if thinking content is provided
	if opts.Thinking != nil && opts.Thinking.Content != "" {
		var thinkingSig *string
		if opts.Thinking.Signature != "" {
			thinkingSig = &opts.Thinking.Signature
		}

		slog.Info("[SaveMessage] Creating thinking block",
			"message_id", messageID,
			"thinking_len", len(opts.Thinking.Content),
			"has_signature", opts.Thinking.Signature != "",
			"position", position)

		block := &db.MessageContentBlock{
			ID:               uuid.New().String(),
			MessageID:        messageID,
			Position:         position,
			BlockType:        reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING,
			Content:          &opts.Thinking.Content,
			ThoughtSignature: thinkingSig,
			Version:          ptr.Of(1),
			CreatedAt:        timestamp,
			UpdatedAt:        timestamp,
		}
		if err := s.repo.CreateContentBlock(ctx, block); err != nil {
			return fmt.Errorf("failed to create thinking content block: %w", err)
		}
		position++
	}

	// Create text block if content is provided
	if opts.Content != "" {
		block := &db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: messageID,
			Position:  position,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &opts.Content,
			Version:   ptr.Of(1),
			CreatedAt: timestamp,
			UpdatedAt: timestamp,
		}
		if err := s.repo.CreateContentBlock(ctx, block); err != nil {
			return fmt.Errorf("failed to create text content block: %w", err)
		}
		position++
	}

	// Create tool_call blocks
	for i, toolCall := range opts.ToolCalls {
		var thoughtSig *string
		if toolCall.ThoughtSignature != "" {
			thoughtSig = &toolCall.ThoughtSignature
		}

		block := &db.MessageContentBlock{
			ID:               uuid.New().String(),
			MessageID:        messageID,
			Position:         position + i,
			BlockType:        reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolName:         &toolCall.Name,
			ToolCallID:       &toolCall.ID,
			ToolInput:        &toolCall.Input,
			ThoughtSignature: thoughtSig,
			Version:          ptr.Of(1),
			CreatedAt:        timestamp,
			UpdatedAt:        timestamp,
		}
		if err := s.repo.CreateContentBlock(ctx, block); err != nil {
			return fmt.Errorf("failed to create tool_call block %d: %w", i, err)
		}
	}

	return nil
}

// createToolContentBlocks creates content blocks for tool messages.
func (s *Service) createToolContentBlocks(ctx context.Context, messageID string, opts SaveMessageOpts, timestamp time.Time) error {
	for i, result := range opts.ToolResults {
		block := &db.MessageContentBlock{
			ID:         uuid.New().String(),
			MessageID:  messageID,
			Position:   i,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &result.ToolCallID,
			ToolName:   ptr.StringIfNotEmpty(result.Name),
			Content:    &result.Content,
			IsError:    &result.IsError,
			Version:    ptr.Of(1),
			CreatedAt:  timestamp,
			UpdatedAt:  timestamp,
		}
		if err := s.repo.CreateContentBlock(ctx, block); err != nil {
			return fmt.Errorf("failed to create tool_result block %d: %w", i, err)
		}
	}
	return nil
}

// createSystemContentBlocks creates content blocks for system messages.
func (s *Service) createSystemContentBlocks(ctx context.Context, messageID string, opts SaveMessageOpts, timestamp time.Time) error {
	if opts.Content != "" {
		block := &db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: messageID,
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &opts.Content,
			Version:   ptr.Of(1),
			CreatedAt: timestamp,
			UpdatedAt: timestamp,
		}
		if err := s.repo.CreateContentBlock(ctx, block); err != nil {
			return fmt.Errorf("failed to create text content block: %w", err)
		}
	}
	return nil
}

// emitChatUpdate emits a chat_update for the frontend.
func (s *Service) emitChatUpdate(ctx context.Context, opts SaveMessageOpts, messageID string, ordinal int64, contextSequence int, threadTokenCount int, timestamp time.Time) error {
	// Fetch content blocks
	blocks, err := s.repo.ListContentBlocks(ctx, messageID)
	if err != nil {
		return fmt.Errorf("failed to list content blocks for chat_update: %w", err)
	}

	// Build content blocks array and collect attachment IDs
	contentBlocks := []map[string]interface{}{}
	attachmentIDs := []string{}
	for _, block := range blocks {
		blockData := map[string]interface{}{
			"id":    block.ID,
			"type":  block.BlockType,
			"index": block.Position,
		}

		if block.Content != nil {
			blockData["content"] = *block.Content
			if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE {
				attachmentIDs = append(attachmentIDs, *block.Content)
			}
		}
		if block.ToolName != nil {
			blockData["tool_name"] = *block.ToolName
		}
		if block.ToolInput != nil {
			blockData["input"] = *block.ToolInput
		}
		if block.ToolCallID != nil {
			blockData["tool_call_id"] = *block.ToolCallID
		}
		if block.IsError != nil {
			blockData["is_error"] = *block.IsError
		}

		contentBlocks = append(contentBlocks, blockData)
	}

	// Fetch attachment metadata
	attachments := []map[string]interface{}{}
	if len(attachmentIDs) > 0 {
		attachmentsData, err := s.repo.GetAttachmentsByIDs(ctx, attachmentIDs)
		if err != nil {
			slog.Warn("Failed to fetch attachments for chat_update", "error", err)
		} else {
			attachmentMap := make(map[string]*db.Attachment)
			for _, att := range attachmentsData {
				attachmentMap[att.ID] = att
			}
			for _, attID := range attachmentIDs {
				if att, found := attachmentMap[attID]; found {
					attachments = append(attachments, map[string]interface{}{
						"id":        att.ID,
						"filename":  att.Filename,
						"size":      att.Size,
						"mime_type": att.MimeType,
						"url":       fmt.Sprintf("/api/attachments/%s", att.ID),
					})
				} else {
					slog.Warn("attachment not found in database, skipping",
						"attachment_id", attID,
						"message_id", messageID,
					)
				}
			}
		}
	}

	// Build update data
	updateData := map[string]interface{}{
		"update_type":          "message",
		"id":                   messageID,
		"role":                 opts.Role,
		"ordinal":              ordinal,
		"thread":               opts.Thread,
		"context_sequence":     contextSequence,
		"created_at":           timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"),
		"updated_at":           timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"),
		"content_blocks":       contentBlocks,
		"attachments":          attachments,
		"thread_token_count":   threadTokenCount,
		"compaction_threshold": DefaultCompactionThreshold,
	}

	// Add optional fields
	if opts.DisplayStyle != 0 {
		updateData["display_style"] = opts.DisplayStyle
	}
	if opts.TokenCount > 0 {
		updateData["token_count"] = opts.TokenCount
	}

	// Marshal and emit
	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		slog.Error("[SaveMessage] Failed to marshal chat_update data",
			"error", err,
			"chat_id", opts.ChatID,
			"message_id", messageID)
		return fmt.Errorf("failed to marshal chat_update data: %w", err)
	}

	if err := s.repo.CreateChatUpdate(ctx, opts.ChatID, db.UpdateTypeMessage, messageID, string(updateDataJSON)); err != nil {
		slog.Error("[SaveMessage] Failed to create chat_update",
			"error", err,
			"chat_id", opts.ChatID,
			"message_id", messageID)
		return fmt.Errorf("failed to create chat_update: %w", err)
	}

	slog.Debug("[SaveMessage] Emitted chat_update",
		"chat_id", opts.ChatID,
		"message_id", messageID,
		"role", opts.Role,
		"ordinal", ordinal,
		"blocks", len(contentBlocks),
		"json_bytes", len(updateDataJSON))

	return nil
}

func displayStylePtrIfNonZero(i int32) *reliantv1.DisplayStyle {
	if i == 0 {
		return nil
	}
	value := reliantv1.DisplayStyle(i)
	return &value
}
