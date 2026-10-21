// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/store"
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

func (r *Repo) ListRuns(ctx context.Context, filters RunListFilters) ([]*RunListItem, bool, error) {
	return r.runs.ListRuns(ctx, filters)
}

func (r *Repo) LastRunPerWorkflow(ctx context.Context, filters RunListFilters) ([]*RunListItem, error) {
	return r.runs.LastRunPerWorkflow(ctx, filters)
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
	return store.InsertMessage(ctx, r.ORM(), msg)
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
	_, err := store.InsertMessageOnce(ctx, r.ORM(), msg)
	return err
}

// CreateMessageOnce creates a message record unless a row already holds its id,
// and reports which happened. Unlike CreateMessageIfNotExists the caller is
// told, because a caller that lost the race must not go on to write the rest
// of the message (content blocks, the chat_update) a second time.
//
// WARNING: Same caveats as CreateMessage - prefer SaveMessageToThreadOnce.
func (r *Repo) CreateMessageOnce(ctx context.Context, msg *Message) (bool, error) {
	if msg == nil {
		return false, fmt.Errorf("message cannot be nil")
	}
	if msg.ID == "" {
		return false, fmt.Errorf("message ID cannot be empty")
	}
	if msg.ChatID == "" {
		return false, fmt.Errorf("message ChatID cannot be empty")
	}
	return store.InsertMessageOnce(ctx, r.ORM(), msg)
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
	return r.SaveMessageToThreadWithID(ctx, chatID, thread, role, content, workflowID, attachmentIDs, displayStyle, "")
}

// SaveMessageToThreadWithID is SaveMessageToThread with a caller-supplied
// message id (delta identity protocol). Empty messageID generates a uuid.
func (r *Repo) SaveMessageToThreadWithID(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string) (*Message, error) {
	msg, _, err := r.saveMessageToThread(ctx, chatID, thread, role, content, workflowID, attachmentIDs, displayStyle, messageID, false)
	return msg, err
}

// SaveMessageToThreadOnce saves a message under an id the CLIENT chose, and is
// safe to repeat: when a row already holds the id it writes nothing (no
// message, no content blocks, no chat_update) and returns created=false with a
// nil message. The id is the arbiter between two repeats racing each other, so
// exactly one of them creates the row.
//
// It does not check that the existing row is the same message or is in the
// same chat; the caller decides what a taken id means.
func (r *Repo) SaveMessageToThreadOnce(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string) (*Message, bool, error) {
	if messageID == "" {
		return nil, false, fmt.Errorf("messageID cannot be empty")
	}
	return r.saveMessageToThread(ctx, chatID, thread, role, content, workflowID, attachmentIDs, displayStyle, messageID, true)
}

func (r *Repo) saveMessageToThread(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string, once bool) (*Message, bool, error) {
	if chatID == "" {
		return nil, false, fmt.Errorf("chatID cannot be empty")
	}
	if thread == "" {
		return nil, false, fmt.Errorf("thread cannot be empty")
	}
	if role == 0 {
		return nil, false, fmt.Errorf("role cannot be unspecified")
	}

	// A message's workflow_id must name the workflow running THIS thread.
	//
	// Callers pass the workflow they are operating on, which for a message
	// addressed to a spawned thread is the PARENT's root workflow — the spawn
	// runs inline inside it and has no Temporal execution of its own. Storing
	// that made a "continue" sent to a spawn look like the thread had changed
	// workflows, and the timeline draws a "handoff" divider on exactly that
	// signal (workflow_id changed on the same thread). Measured here: 203 user
	// messages across 51 chats carried the parent's id, and those accounted for
	// every one of the 7 threads that appeared to span multiple workflows.
	//
	// The thread's own workflow row is the authority, so ask it. Best-effort:
	// if no workflow row exists for this thread yet (the first message of a
	// brand-new thread), the caller's value stands.
	if workflowID != nil && *workflowID != "" && *workflowID != thread {
		if wf, err := r.GetWorkflowByThread(ctx, chatID, thread); err == nil && wf != nil && wf.ID != "" {
			owning := wf.ID
			workflowID = &owning
		}
	}

	var savedMsg *Message
	created := true
	err := r.RunTx(ctx, func(txCtx context.Context) error {
		// The transaction is retried on a serialization conflict, and a retry
		// starts over.
		savedMsg, created = nil, true

		// Get next ordinal for the thread
		ordinal, err := r.GetNextOrdinal(txCtx, thread)
		if err != nil {
			return fmt.Errorf("failed to get next ordinal: %w", err)
		}

		// Get next chat-global seq. See 20260802000000_add_message_seq.sql.
		seq, err := r.GetNextSeq(txCtx, chatID, thread)
		if err != nil {
			return fmt.Errorf("failed to get next seq: %w", err)
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
		if messageID != "" {
			msgID = messageID
		}
		msg := &Message{
			ID:              msgID,
			ChatID:          chatID,
			Ordinal:         ordinal,
			Seq:             seq,
			ThreadID:        thread,
			ContextWindowID: contextWindowID,
			Role:            reliantv1.MessageRole(role),
			DisplayStyle:    int32DisplayStyleToProto(displayStyle),
			WorkflowID:      workflowID,
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		if once {
			// Under SERIALIZABLE a repeat whose twin committed after this
			// snapshot does not get here: Postgres fails the insert with a
			// serialization error and the retry above finds the row already
			// visible. DO NOTHING covers the row that was visible from the
			// start.
			ok, err := r.CreateMessageOnce(txCtx, msg)
			if err != nil {
				return fmt.Errorf("failed to create message: %w", err)
			}
			if !ok {
				created = false
				return nil
			}
		} else if err := r.CreateMessage(txCtx, msg); err != nil {
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
		updateData := MessageUpdateData{
			UpdateType: "message",
			ID:         msgID,
			Role:       role,
			// seq is what the client sorts by; without it this message
			// deserializes at seq 0 and jumps to the top of the transcript.
			Seq:             seq,
			Ordinal:         ordinal,
			Thread:          thread,
			ContextSequence: &contextSequence,
			CreatedAt:       now.Format("2006-01-02T15:04:05.999999999Z07:00"),
			UpdatedAt:       now.Format("2006-01-02T15:04:05.999999999Z07:00"),
			ContentBlocks:   contentBlocks,
			Attachments:     &attachments,
			// A HIDDEN message is LLM-context only, and both filters that
			// enforce that (ListMessages and the timeline) read display_style.
			// Omitting it here delivers the message with displayStyle
			// undefined, so it renders live and only disappears on reload.
			DisplayStyle: displayStyle,
		}

		updateDataJSON, err := updateData.Marshal()
		if err != nil {
			return fmt.Errorf("failed to marshal chat_update data: %w", err)
		}

		if err := r.CreateChatUpdate(txCtx, chatID, UpdateTypeMessage, msgID, updateDataJSON); err != nil {
			return fmt.Errorf("failed to create chat_update: %w", err)
		}

		savedMsg = msg
		return nil
	})

	if err != nil {
		return nil, false, err
	}
	return savedMsg, created, nil
}

func (r *Repo) GetMessage(ctx context.Context, id string) (*Message, error) {
	if id == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}
	msg, err := store.FindMessage(ctx, r.ORM(), id)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message not found: %s", id)
	}
	return msg, nil
}

func (r *Repo) GetNextOrdinal(ctx context.Context, threadID string) (int64, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	// Get the next ordinal from messages in this thread (all context windows)
	// NOTE: For workflow forks, each workflow has its own ordinal space.
	// Context inheritance is handled separately at LLM context resolution time.
	return store.NextOrdinalInThread(ctx, r.ORM(), threadID)
}

// GetNextSeq returns the next chat-global seq for a chat. See
// 20260802000000_add_message_seq.sql for what seq is and why it exists
// alongside the per-thread Ordinal.
func (r *Repo) GetNextSeq(ctx context.Context, chatID, threadID string) (int64, error) {
	if chatID == "" {
		return 0, fmt.Errorf("chat ID cannot be empty")
	}
	return store.NextSeqInChat(ctx, r.ORM(), chatID, threadID)
}

func (r *Repo) ListMessages(ctx context.Context, chatID string, opts MessageListOptions) ([]*Message, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return store.ListMessages(ctx, r.ORM(), chatID, opts)
}

func (r *Repo) ListRecentTranscriptSiblingMessages(ctx context.Context, chatID, mainThreadID string, fromSeq int64, limit int) ([]*Message, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	if mainThreadID == "" {
		return nil, fmt.Errorf("main thread ID cannot be empty")
	}

	return store.ListRecentTranscriptSiblingMessages(ctx, r.ORM(), chatID, mainThreadID, fromSeq, limit)
}

func (r *Repo) CountMessagesInChat(ctx context.Context, chatID string) (int, error) {
	if chatID == "" {
		return 0, fmt.Errorf("chat ID cannot be empty")
	}

	return r.countMessages(ctx, orm.WhereEq(store.MessageFieldChatID, chatID))
}

func (r *Repo) CountMessagesByContextWindow(ctx context.Context, contextWindowID string) (int, error) {
	if contextWindowID == "" {
		return 0, fmt.Errorf("context window ID cannot be empty")
	}

	return r.countMessages(ctx, orm.WhereEq(store.MessageFieldContextWindowID, contextWindowID))
}

func (r *Repo) CountMessagesByContextWindowUpToSeq(ctx context.Context, contextWindowID string, maxSeq int64) (int, error) {
	if contextWindowID == "" {
		return 0, fmt.Errorf("context window ID cannot be empty")
	}

	return r.countMessages(ctx, orm.WhereEq(store.MessageFieldContextWindowID, contextWindowID), orm.WhereLte(store.MessageFieldSeq, maxSeq))
}

func (r *Repo) ListRecentMessagesInContextWindowBeforeSeq(ctx context.Context, contextWindowID string, beforeSeq int64, limit int) ([]*Message, error) {
	if contextWindowID == "" {
		return nil, fmt.Errorf("context window ID cannot be empty")
	}

	return store.ListRecentMessagesInContextWindowBeforeSeq(ctx, r.ORM(), contextWindowID, beforeSeq, limit)
}

func (r *Repo) HasMessagesBeforeInContextWindow(ctx context.Context, contextWindowID string, beforeSeq int64) (bool, error) {
	if contextWindowID == "" {
		return false, fmt.Errorf("context window ID cannot be empty")
	}

	return store.HasMessagesBeforeInContextWindow(ctx, r.ORM(), contextWindowID, beforeSeq)
}

func (r *Repo) ListMessagesInContextWindowRange(ctx context.Context, contextWindowID string, fromSeq int64, toSeq *int64) ([]*Message, error) {
	if contextWindowID == "" {
		return nil, fmt.Errorf("context window ID cannot be empty")
	}

	return store.ListMessagesInContextWindowRange(ctx, r.ORM(), contextWindowID, fromSeq, toSeq)
}

func (r *Repo) GetLatestMessageInThread(ctx context.Context, threadID string) (*Message, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	return store.LatestMessageInThread(ctx, r.ORM(), threadID)
}

func (r *Repo) ThreadAwaitsReply(ctx context.Context, threadID string) (bool, error) {
	if threadID == "" {
		return false, fmt.Errorf("thread ID cannot be empty")
	}
	turn, err := store.LatestTurnInThread(ctx, r.ORM(), threadID)
	if err != nil {
		return false, fmt.Errorf("failed to get latest turn: %w", err)
	}
	return turn != nil && turn.Role == reliantv1.MessageRole_MESSAGE_ROLE_USER, nil
}

func (r *Repo) GetMaxContextSequenceInThread(ctx context.Context, threadID string) (int, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	maxSeq, err := r.GetMaxSequenceForThread(ctx, threadID)
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
		logging.DebugContext(ctx, "[GetMaxContextSequenceInThread] Error getting thread, defaulting to 0",
			"threadID", threadID,
			"error", err)
		return 0, nil
	}

	if threadRecord != nil && threadRecord.ParentThreadID != nil && threadRecord.ForkAtMessageID != nil {
		// Get the parent's context_window sequence at the fork point, via the
		// fork message's own context window.
		forkMsg, err := r.GetMessage(ctx, *threadRecord.ForkAtMessageID)
		if err == nil && forkMsg != nil {
			parentCW, err := r.GetContextWindow(ctx, forkMsg.ContextWindowID)
			if err == nil && parentCW != nil {
				logging.DebugContext(ctx, "[GetMaxContextSequenceInThread] Inheriting context_sequence from parent fork point",
					"threadID", threadID,
					"forkAtMessageID", *threadRecord.ForkAtMessageID,
					"parentContextWindowID", forkMsg.ContextWindowID,
					"inheritedContextSeq", parentCW.Sequence)
				return parentCW.Sequence, nil
			}
		}
		logging.DebugContext(ctx, "[GetMaxContextSequenceInThread] Failed to get parent context_window at fork point",
			"threadID", threadID,
			"parentThreadID", *threadRecord.ParentThreadID,
			"forkAtMessageID", *threadRecord.ForkAtMessageID,
			"error", err)
	}

	return 0, nil
}

func (r *Repo) GetLatestMessageWithTokensInThread(ctx context.Context, threadID string, contextSequence int) (*Message, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	return store.LatestMessageWithTokens(ctx, r.ORM(), threadID, int64(contextSequence), nil)
}

func (r *Repo) CountMessagesInThread(ctx context.Context, threadID string) (int, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	return r.countMessages(ctx, orm.WhereEq(store.MessageFieldThreadID, threadID))
}

// GetMessagesByContextWindow loads messages from a specific context window.
// This is used by the threads package for CW chain resolution.
func (r *Repo) GetMessagesByContextWindow(ctx context.Context, contextWindowID string, maxOrdinal *int64) ([]*Message, error) {
	// For now, use the legacy approach with thread + context_sequence
	// This will be updated when we migrate messages to use context_window_id directly
	cw, chatID, _, _, err := r.GetContextWindowWithThread(ctx, contextWindowID)
	if err != nil {
		return nil, fmt.Errorf("failed to get context window: %w", err)
	}

	opts := MessageListOptions{
		Thread:          &cw.ThreadID,
		ContextSequence: &cw.Sequence,
	}

	msgs, err := r.ListMessages(ctx, chatID, opts)
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
	return store.InsertContentBlock(ctx, r.ORM(), block)
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
	return store.InsertContentBlockOnce(ctx, r.ORM(), block)
}

func (r *Repo) GetContentBlock(ctx context.Context, id string) (*MessageContentBlock, error) {
	if id == "" {
		return nil, fmt.Errorf("content block ID cannot be empty")
	}
	block, err := store.FindContentBlock(ctx, r.ORM(), id)
	if err != nil {
		return nil, fmt.Errorf("failed to get content block: %w", err)
	}
	if block == nil {
		return nil, fmt.Errorf("content block not found: %s", id)
	}
	return block, nil
}

// GetContentBlockByToolCallID finds a content block by its tool_call_id
// Returns an error if the block is not found or if the tool_call_id is empty
func (r *Repo) GetContentBlockByToolCallID(ctx context.Context, toolCallID string) (*MessageContentBlock, error) {
	if toolCallID == "" {
		return nil, fmt.Errorf("tool call ID cannot be empty")
	}
	block, err := store.FindToolBlock(ctx, r.ORM(), toolCallID, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL)
	if err != nil {
		return nil, fmt.Errorf("failed to get content block by tool_call_id: %w", err)
	}
	if block == nil {
		return nil, fmt.Errorf("tool call not found: %s", toolCallID)
	}
	return block, nil
}

func (r *Repo) ListContentBlocks(ctx context.Context, messageID string) ([]*MessageContentBlock, error) {
	if messageID == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}
	return store.ListContentBlocks(ctx, r.ORM(), messageID)
}

func (r *Repo) ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*MessageContentBlock, error) {
	if len(messageIDs) == 0 {
		return []*MessageContentBlock{}, nil
	}
	return store.ListContentBlocksForMessages(ctx, r.ORM(), messageIDs)
}

func (r *Repo) UpdateContentBlock(ctx context.Context, block *MessageContentBlock) error {
	if block == nil {
		return fmt.Errorf("content block cannot be nil")
	}
	if block.ID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}
	return store.UpdateContentBlockBody(ctx, r.ORM(), block)
}

func (r *Repo) AppendToContentBlock(ctx context.Context, blockID string, delta string) error {
	if blockID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}
	return store.AppendToContentBlock(ctx, r.ORM(), blockID, delta)
}

// AppendContentBlockDelta appends text delta to a content block's content field
func (r *Repo) AppendContentBlockDelta(ctx context.Context, chatID string, blockID string, delta string) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	if blockID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}
	return store.AppendContentDelta(ctx, r.ORM(), blockID, delta)
}

// AppendToolInputDelta appends JSON delta to a content block's tool_input field
func (r *Repo) AppendToolInputDelta(ctx context.Context, chatID string, blockID string, delta string) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	if blockID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}
	return store.AppendToolInputDelta(ctx, r.ORM(), blockID, delta)
}

// UpdateMessage updates a message (implements Repository interface)
func (r *Repo) UpdateMessage(ctx context.Context, msg *Message) error {
	if msg == nil {
		return fmt.Errorf("message cannot be nil")
	}
	if msg.ID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}

	return store.UpdateMessageUsage(ctx, r.ORM(), msg.ID, msg.TokenCount, msg.Cost)
}

// UpdateMessageFields updates a message with the provided map of field updates
func (r *Repo) UpdateMessageFields(ctx context.Context, messageID string, updates map[string]interface{}) error {
	if messageID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}
	return store.UpdateMessageFields(ctx, r.ORM(), messageID, updates)
}

// MarkMessageBlocksComplete marks all content blocks for a message as complete
// This is called when streaming finishes to update timestamps
// Note: streaming_state field has been removed - state is now computed from content blocks
func (r *Repo) MarkMessageBlocksComplete(ctx context.Context, messageID string) error {
	if messageID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}

	blocks, err := store.TouchMessageBlocks(ctx, r.ORM(), messageID)
	if err != nil {
		return fmt.Errorf("failed to mark blocks complete: %w", err)
	}
	if blocks > 0 {
		logging.DebugContext(ctx, "Marked message blocks as complete",
			"messageID", messageID,
			"blocksUpdated", blocks)
	}
	return nil
}

// DeleteMessage deletes a message and cascades to content blocks (ON DELETE CASCADE)
func (r *Repo) DeleteMessage(ctx context.Context, messageID string) error {
	if messageID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}
	return store.RemoveMessage(ctx, r.ORM(), messageID)
}

// ==================== Approval Methods ====================

// CreateApproval creates a new approval record and emits an activity event if the activity changed.
func (r *Repo) CreateApproval(ctx context.Context, approval *Approval) error {
	if err := store.CreateApprovalIfAbsent(ctx, r.ORM(), approval); err != nil {
		return err
	}
	// Activity may have changed from RUNNING → AWAITING_INPUT
	return r.emitChatActivityIfChanged(ctx, approval.ChatID)
}

// GetApproval retrieves an approval by ID
func (r *Repo) GetApproval(ctx context.Context, id string) (*Approval, error) {
	return store.GetApproval(ctx, r.ORM(), id)
}

// GetApprovalByEntityID retrieves an approval by its entity_id
func (r *Repo) GetApprovalByEntityID(ctx context.Context, entityID string) (*Approval, error) {
	return store.GetApprovalByEntityID(ctx, r.ORM(), entityID)
}

// ListPendingApprovalsByChat retrieves all pending approvals for a chat
func (r *Repo) ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*Approval, error) {
	return store.ListPendingApprovalsByChat(ctx, r.ORM(), chatID)
}

// ListApprovalsByChat retrieves all approvals for a chat (including resolved)
func (r *Repo) ListApprovalsByChat(ctx context.Context, chatID string) ([]*Approval, error) {
	return store.ListApprovalsByChat(ctx, r.ORM(), chatID)
}

// UpdateApprovalStatus updates the status of an approval and emits an activity event if the activity changed.
func (r *Repo) UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string) error {
	var resolvedAt *time.Time
	if status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED) || status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED) {
		now := time.Now()
		resolvedAt = &now
	}

	if err := store.UpdateApprovalStatus(ctx, r.ORM(), id, status, denialReason, actionTaken, metadata, resolvedAt); err != nil {
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

	return store.InsertProject(ctx, r.ORM(), project)
}

func (r *Repo) GetProject(ctx context.Context, id string) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}

	return store.GetProject(ctx, r.ORM(), id)
}

func (r *Repo) GetProjectByPath(ctx context.Context, path string) (*Project, error) {
	if path == "" {
		return nil, fmt.Errorf("project path cannot be empty")
	}

	return store.GetProjectByPath(ctx, r.ORM(), path)
}

func (r *Repo) GetProjectByPathAndUser(ctx context.Context, path, userID string) (*Project, error) {
	if path == "" {
		return nil, fmt.Errorf("project path cannot be empty")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	return store.GetProjectByPathAndUser(ctx, r.ORM(), path, userID)
}

func (r *Repo) GetProjectByRemoteURLAndUser(ctx context.Context, remoteURL, userID string) (*Project, error) {
	if remoteURL == "" {
		return nil, fmt.Errorf("remote URL cannot be empty")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	return store.GetProjectByRemoteURLAndUser(ctx, r.ORM(), remoteURL, userID)
}

func (r *Repo) GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	return store.GetProjectWithUserCheck(ctx, r.ORM(), id, userID)
}

func (r *Repo) ListProjects(ctx context.Context, filters ProjectFilters) ([]*Project, error) {
	return store.ListProjects(ctx, r.ORM(), filters)
}

func (r *Repo) UpdateProject(ctx context.Context, project *Project, userID string) error {
	if project == nil {
		return fmt.Errorf("project cannot be nil")
	}
	if project.ID == "" {
		return fmt.Errorf("project ID cannot be empty")
	}

	return store.ReplaceProject(ctx, r.ORM(), project, userID)
}

func (r *Repo) SetProjectForgeName(ctx context.Context, id, userID, forgeProjectName string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("project ID cannot be empty")
	}
	if strings.TrimSpace(forgeProjectName) == "" {
		return false, fmt.Errorf("forge project name cannot be empty")
	}
	return store.SetProjectForgeName(ctx, r.ORM(), id, userID, forgeProjectName)
}
func (r *Repo) TouchProject(ctx context.Context, id string, userID string) error {
	if id == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	return store.TouchProject(ctx, r.ORM(), id, userID)
}

func (r *Repo) DeleteProject(ctx context.Context, id string, userID string) error {
	if id == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	return store.DeleteUserProject(ctx, r.ORM(), id, userID)
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
	return store.RecordProjectDaemonInstalled(ctx, r.ORM(), projectID, daemonID, path, defaultBranch)
}

// UpsertQueuedProjectDaemon records a clone that is queued but has not run.
func (r *Repo) UpsertQueuedProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, requestID string) error {
	if projectID == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}
	if requestID == "" {
		// Without it the outcome notification can never find this row, and
		// the project would sit in "installing" forever.
		return fmt.Errorf("request ID cannot be empty for a queued clone")
	}
	return store.RecordProjectDaemonQueued(ctx, r.ORM(), projectID, daemonID, path, defaultBranch, requestID)
}

// ResolveQueuedProjectDaemon applies the outcome the daemon reported.
func (r *Repo) ResolveQueuedProjectDaemon(ctx context.Context, requestID, installErr string) error {
	if requestID == "" {
		return fmt.Errorf("request ID cannot be empty")
	}
	return store.ResolveQueuedProjectDaemon(ctx, r.ORM(), requestID, installErr)
}

// MarkProjectDaemonInstalled settles a queued clone from a filesystem
// announcement, which names the path but carries no request id.
func (r *Repo) MarkProjectDaemonInstalled(ctx context.Context, projectID, daemonID string) error {
	if projectID == "" || daemonID == "" {
		return fmt.Errorf("project ID and daemon ID cannot be empty")
	}
	return store.MarkProjectDaemonInstalled(ctx, r.ORM(), projectID, daemonID)
}

func (r *Repo) ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return store.ListProjectDaemonsForProject(ctx, r.ORM(), projectID)
}

func (r *Repo) ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemon, error) {
	if daemonID == "" {
		return nil, fmt.Errorf("daemon ID cannot be empty")
	}
	return store.ListProjectDaemonsForDaemon(ctx, r.ORM(), daemonID)
}

func (r *Repo) DeleteProjectDaemon(ctx context.Context, projectID, daemonID string) error {
	if projectID == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	return store.RemoveProjectDaemon(ctx, r.ORM(), projectID, daemonID)
}

// ErrDaemonOwnedByAnotherUser is returned when a daemon write names an id whose
// row (or attachment) already belongs to a different user. Ownership is
// immutable once the row exists.
var ErrDaemonOwnedByAnotherUser = errors.New("daemon is owned by another user")

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
	createdAt := daemon.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	wrote, err := store.UpsertDaemonRegistration(ctx, r.ORM(), daemon, createdAt, now)
	if err != nil {
		return fmt.Errorf("failed to upsert daemon %s: %w", daemon.ID, err)
	}
	// The conflict branch only fires for the row's own owner, so a conflict
	// against a different owner writes nothing — in a single statement, with
	// no read-then-write window.
	if !wrote {
		return fmt.Errorf("%w: daemon %s", ErrDaemonOwnedByAnotherUser, daemon.ID)
	}
	return nil
}

func (r *Repo) GetDaemon(ctx context.Context, id string) (*Daemon, error) {
	if id == "" {
		return nil, fmt.Errorf("daemon ID cannot be empty")
	}
	d, err := store.GetDaemon(ctx, r.ORM(), id)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get daemon %s: %w", id, err)
	}
	return d, err
}

// ApplyDaemonLifecycle mirrors one control-plane lifecycle event onto an
// existing daemons row.
//
// The WHERE clause is the whole point: last_status_changed_at is compared, not
// just written. Lifecycle events travel on plain NATS, which is newest-wins
// with no redelivery and no ordering guarantee, so a delayed "provisioning"
// can arrive after the "ready" that superseded it. Dropping it in SQL means
// the guard holds across every replica and every retry, not only within one
// consumer goroutine.
func (r *Repo) ApplyDaemonLifecycle(ctx context.Context, lc DaemonLifecycleUpdate) (bool, error) {
	if lc.DaemonID == "" {
		return false, fmt.Errorf("daemon ID cannot be empty")
	}
	if lc.ChangedAt.IsZero() {
		return false, fmt.Errorf("lifecycle changed_at cannot be zero")
	}
	applied, err := store.ApplyDaemonLifecycle(ctx, r.ORM(), lc)
	if err != nil {
		return false, fmt.Errorf("failed to apply daemon lifecycle for %s: %w", lc.DaemonID, err)
	}
	return applied, nil
}

func (r *Repo) ListDaemonsByUserID(ctx context.Context, userID string) ([]*Daemon, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	daemons, err := store.ListDaemonsByUserID(ctx, r.ORM(), userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list daemons for user %s: %w", userID, err)
	}
	return daemons, nil
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
	wrote, err := store.UpsertDaemonAttachment(ctx, r.ORM(), att)
	if err != nil {
		return fmt.Errorf("upserting daemon attachment: %w", err)
	}
	// A different user's lease is only replaceable by the daemon row's owner
	// (which heals a lease a past hijack left behind).
	if !wrote {
		return fmt.Errorf("%w: attachment for daemon %s", ErrDaemonOwnedByAnotherUser, att.DaemonID)
	}
	return nil
}

// RenewDaemonAttachment renews the lease of a stream the caller is holding
// RIGHT NOW — the gateway calls it on every heartbeat its receive loop reads
// — re-creating the row if it is gone, and reports whether it had to.
//
// Re-creating it is the point. The lease used to be renewed with an UPDATE,
// which matches nothing once the row is gone, so a lease lost while its stream
// lived stayed lost for the rest of the session: the TTL reaper
// (daemonstate.AttachmentTTL) expiring it during a 15-minute inbound stall, or
// a connect-time upsert that never landed. Prod 2026-10-11: a daemon serving
// terminal commands for six hours read DISCONNECTED to every chat because its
// lease was reaped at 12:11 and nothing could write it back short of a
// reconnect. Routing does not read the lease, which is why the machine kept
// working while every chat said it was gone.
//
// The row only moves forward. last_stream_activity and attached_at each keep
// the later of the stored and renewed values, so an out-of-order renewal is a
// no-op, and a renewal from a session NEWER than the row's (its own connect
// upsert never replaced an older session's row) adopts the row: that older
// session's release (ReleaseDaemonAttachment, attached_at <= its own) can then
// no longer delete the lease the live session is renewing. Memory telemetry
// and detected ports are left alone — they belong to the registered session,
// and the gateway rewrites them on its next heartbeat.
//
// The insert is fenced on the daemons row still belonging to the user, the
// same ownership rule UpsertDaemonAttachment enforces: a daemon the control
// plane deleted while its stream was up is not resurrected by a heartbeat, and
// another user's lease is never touched. Either refusal leaves nothing of the
// caller's to renew, so it is not an error: created is false.
//
// created reports that the row was inserted: the lease was missing while its
// stream was live, and every reader had been reporting the daemon detached.
func (r *Repo) RenewDaemonAttachment(ctx context.Context, att *DaemonAttachment) (created bool, err error) {
	if att == nil || att.DaemonID == "" || att.UserID == "" || att.Source == "" {
		return false, fmt.Errorf("renewing daemon attachment requires daemon_id, user_id and source")
	}
	// A renewal names its session (attached_at). Defaulting it would make a
	// renewal look like a newer session and adopt — and so outlive — a lease
	// its real session can then no longer release.
	if att.AttachedAt.IsZero() || att.LastStreamActivity.IsZero() {
		return false, fmt.Errorf("renewing daemon attachment requires attached_at and last_stream_activity")
	}
	created, err = store.RenewDaemonAttachment(ctx, r.ORM(), att)
	if err != nil {
		return false, fmt.Errorf("renewing daemon attachment: %w", err)
	}
	return created, nil
}

// UpdateDaemonAttachmentMemory records the workspace memory telemetry carried
// by a daemon heartbeat on the attachment (liveness) record. A no-op when the
// attachment row doesn't exist — telemetry is only meaningful for a live
// stream. It deliberately does NOT touch last_stream_activity; the lease
// renewal stays with RenewDaemonAttachment.
func (r *Repo) UpdateDaemonAttachmentMemory(ctx context.Context, daemonID string, usedBytes, limitBytes int64, pressure bool) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	if err := store.SetDaemonAttachmentMemory(ctx, r.ORM(), daemonID, usedBytes, limitBytes, pressure); err != nil {
		return fmt.Errorf("updating daemon attachment memory: %w", err)
	}
	return nil
}

// UpdateDaemonAttachmentPorts records the heartbeat-reported detected
// listener ports on the attachment (liveness) record. Mirrors
// UpdateDaemonAttachmentMemory: a no-op when the attachment row doesn't
// exist, and it deliberately does NOT touch last_stream_activity — the lease
// renewal stays with RenewDaemonAttachment.
func (r *Repo) UpdateDaemonAttachmentPorts(ctx context.Context, daemonID string, ports []uint32) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	if err := store.SetDaemonAttachmentPorts(ctx, r.ORM(), daemonID, ports); err != nil {
		return fmt.Errorf("updating daemon attachment ports: %w", err)
	}
	return nil
}

// SetDaemonLocalModels stores the daemon's last-published local model
// inventory (protojson of reliant.v1.LocalModelInventory). The value replaces
// the previous one wholesale: the daemon always publishes its complete view.
func (r *Repo) SetDaemonLocalModels(ctx context.Context, daemonID string, inventoryJSON string) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	if err := store.SetDaemonLocalModels(ctx, r.ORM(), daemonID, inventoryJSON); err != nil {
		return fmt.Errorf("updating daemon local models: %w", err)
	}
	return nil
}

// ListDaemonLocalModels returns the stored local model inventory of every
// daemon the user owns that has published one, keyed by daemon ID. Liveness is
// NOT considered here; callers gate on the daemon being online before offering
// a model for use.
func (r *Repo) ListDaemonLocalModels(ctx context.Context, userID string) (map[string]string, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	inventories, err := store.ListDaemonLocalModels(ctx, r.ORM(), userID)
	if err != nil {
		return nil, fmt.Errorf("listing daemon local models for user %s: %w", userID, err)
	}
	return inventories, nil
}

// GetSettingValue returns a user-level setting's value, and whether it is set.
func (r *Repo) GetSettingValue(ctx context.Context, userID, key string) (string, bool) {
	setting, err := r.GetSetting(ctx, userID, nil, key)
	if err != nil || setting == nil {
		return "", false
	}
	return setting.Value, true
}

// TryAdvisoryLock takes a session-level advisory lock on a dedicated
// connection, so exactly one replica at a time holds it. The lock dies with the
// connection, so a crashed holder never strands it.
//
// Session-level locks need a connection that stays the same backend for the
// whole session. That holds on direct Postgres and with session-pooled
// pgbouncer; it does NOT hold behind transaction-pooling pgbouncer, where the
// lock can be taken on one backend and released (or silently re-entered by
// another replica) on another. So the connection comes from sessionPool: the
// DatabaseConfig.DirectURL pool when the main pool is transaction-pooled.
func (r *Repo) TryAdvisoryLock(ctx context.Context, key int64) (func(), bool, error) {
	conn, err := r.sessionPool().Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !got {
		_ = conn.Close()
		return func() {}, false, nil
	}
	return func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, key)
		_ = conn.Close()
	}, true, nil
}

// SetDaemonStorageState stores the daemon's last-reported disk state, replacing
// the previous value.
func (r *Repo) SetDaemonStorageState(ctx context.Context, daemonID string, stateJSON string) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	if err := store.SetDaemonStorageState(ctx, r.ORM(), daemonID, stateJSON); err != nil {
		return fmt.Errorf("updating daemon storage state: %w", err)
	}
	return nil
}

// GetDaemonStorageState returns the daemon's stored disk state; "" when it has
// none or there is no such daemon.
func (r *Repo) GetDaemonStorageState(ctx context.Context, daemonID string) (string, error) {
	if daemonID == "" {
		return "", fmt.Errorf("daemon ID cannot be empty")
	}
	state, err := store.GetDaemonStorageState(ctx, r.ORM(), daemonID)
	if err != nil {
		return "", fmt.Errorf("reading daemon storage state: %w", err)
	}
	return state, nil
}

// ListDaemonStorageState returns the stored disk state of every daemon the user
// owns that has reported one, keyed by daemon ID.
func (r *Repo) ListDaemonStorageState(ctx context.Context, userID string) (map[string]string, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	states, err := store.ListDaemonStorageState(ctx, r.ORM(), userID)
	if err != nil {
		return nil, fmt.Errorf("listing daemon storage state for user %s: %w", userID, err)
	}
	return states, nil
}

// ReleaseDaemonAttachment deletes the lease a stream session created when
// that session ends: the row for daemonID whose attached_at is at or before
// the session's own attachedAt.
//
// The bound is what keeps a stale teardown from deleting a newer session's
// lease. A daemon id is held by one stream at a time, but not by one PROCESS:
// a daemon that loses its gateway reconnects to another replica, while the old
// replica may only notice its half of the stream died a sweep later — and an
// unconditional delete by daemon_id then removed the lease the NEW session had
// just written. Rows older than the session (one its connect-time upsert
// failed to replace) are released with it; rows newer than it are not its to
// delete.
//
// attached_at comes from the clock of the gateway that wrote it. One daemon's
// sessions are strictly sequential (a session ends before the daemon redials),
// so ordering them only needs the replicas' clocks to agree to within one
// reconnect.
//
// The released lease's last_stream_activity moves onto daemons.last_seen_at in
// the same statement: deleting the lease is otherwise the moment the registry
// forgets the daemon was ever there, and a tool call deciding whether to wait
// for a daemon that just dropped (DaemonLastSeenAt) needs exactly that fact.
// It only moves forward, so a stale session's release cannot rewind it.
func (r *Repo) ReleaseDaemonAttachment(ctx context.Context, daemonID string, attachedAt time.Time) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	if attachedAt.IsZero() {
		return fmt.Errorf("releasing daemon attachment %s requires the session's attached_at", daemonID)
	}
	if err := store.ReleaseDaemonAttachment(ctx, r.ORM(), daemonID, attachedAt); err != nil {
		return fmt.Errorf("releasing daemon attachment: %w", err)
	}
	return nil
}

// DaemonLastSeenAt reports the last time a gateway heard from the user's
// daemon: its attachment lease's last_stream_activity while a lease exists,
// else the last_seen_at its most recent released lease left behind. Zero when
// neither exists, or the daemon is not the user's.
//
// A lease outlives its stream when the gateway holding it dies without a
// teardown, so the lease is read whatever its age: a crashed gateway's lease
// is the only record that its daemon was there a moment ago.
func (r *Repo) DaemonLastSeenAt(ctx context.Context, userID, daemonID string) (time.Time, error) {
	if userID == "" || daemonID == "" {
		return time.Time{}, fmt.Errorf("daemon last-seen lookup requires user ID and daemon ID")
	}
	seen, err := store.DaemonLastSeenAt(ctx, r.ORM(), userID, daemonID)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading daemon last seen: %w", err)
	}
	return seen, nil
}

// DeleteStaleDaemonAttachments removes attachment rows whose reachability
// lease (last_stream_activity) has not been renewed for olderThan, and
// returns how many were deleted.
//
// This is the GC for a leak that is structural, not incidental:
// ReleaseDaemonAttachment runs only on graceful teardown, so a gateway that
// crashes, is rescheduled, or is redeployed strands every row it owned with
// nothing left in the world that knows to clean it up. Dev's registry on
// 2026-08-24 still carried 0c9cff04 (29 days stale) and 81dc53c1 (a workspace
// pod deleted 51 days earlier); both had permanently pinned /flow-health at
// 503. Nothing in the in-memory sweeper can fix this — it reaps connections
// this PROCESS holds, and the orphans' processes are long gone.
//
// Safe across replicas without any coordination: a live stream renews its
// lease on every inbound heartbeat (15s), so a row untouched for olderThan
// cannot belong to a live stream on ANY gateway. The DELETE re-evaluates
// last_stream_activity at execution time, so a concurrent reconnect (whose
// upsert sets the lease to now) is never raced away.
func (r *Repo) DeleteStaleDaemonAttachments(ctx context.Context, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		return 0, fmt.Errorf("stale threshold must be positive")
	}
	n, err := store.DeleteStaleDaemonAttachments(ctx, r.ORM(), time.Now().UTC().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("deleting stale daemon attachments: %w", err)
	}
	return n, nil
}

func (r *Repo) IsDaemonAttached(ctx context.Context, userID string, staleThreshold time.Duration) (bool, error) {
	if userID == "" {
		return false, fmt.Errorf("user ID cannot be empty")
	}
	return store.IsUserDaemonAttached(ctx, r.ORM(), userID, time.Now().UTC().Add(-staleThreshold))
}

// IsDaemonIDAttached reports whether the daemon has a fresh attachment lease.
func (r *Repo) IsDaemonIDAttached(ctx context.Context, daemonID string, staleThreshold time.Duration) (bool, error) {
	if daemonID == "" {
		return false, fmt.Errorf("daemon ID cannot be empty")
	}
	return store.IsDaemonIDAttached(ctx, r.ORM(), daemonID, time.Now().UTC().Add(-staleThreshold))
}

func (r *Repo) ListOutboundAttachments(ctx context.Context) ([]*DaemonAttachment, error) {
	return store.ListOutboundAttachments(ctx, r.ORM())
}

// ListAllDaemonAttachments returns every attachment row regardless of source.
func (r *Repo) ListAllDaemonAttachments(ctx context.Context) ([]*DaemonAttachment, error) {
	return store.ListAllDaemonAttachments(ctx, r.ORM())
}

// ListFreshDaemonAttachmentsForUser returns the user's attachment rows whose
// last_stream_activity is within the stale threshold — the full liveness
// records (including memory telemetry) behind ListAttachedDaemonIDsForUser's
// id-only view.
func (r *Repo) ListFreshDaemonAttachmentsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]*DaemonAttachment, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.ListFreshDaemonAttachmentsForUser(ctx, r.ORM(), userID, time.Now().UTC().Add(-staleThreshold))
}

func (r *Repo) ListAttachedDaemonIDsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]string, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.ListAttachedDaemonIDsForUser(ctx, r.ORM(), userID, time.Now().UTC().Add(-staleThreshold))
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

	return store.UpsertProjectConfigRecord(ctx, r.ORM(), record)
}

// GetProjectConfigRecord reads the project's whole config record, or
// sql.ErrNoRows, unwrapped. A caller that needs one part of it reads that
// column instead (see store.GetProjectConfigRecord).
func (r *Repo) GetProjectConfigRecord(ctx context.Context, projectID string) (*ProjectConfigRecord, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return store.GetProjectConfigRecord(ctx, r.ORM(), projectID)
}

// GetProjectWorkflowsJSON reads one column of the project config record: the
// synced .reliant/workflows. It returns sql.ErrNoRows, unwrapped, when the
// project has no record.
func (r *Repo) GetProjectWorkflowsJSON(ctx context.Context, projectID string) (*string, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return store.GetProjectWorkflowsJSON(ctx, r.ORM(), projectID)
}

// GetProjectPresetsJSON reads one column of the project config record: the
// synced .reliant/presets. It returns sql.ErrNoRows, unwrapped, when the
// project has no record.
func (r *Repo) GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return store.GetProjectPresetsJSON(ctx, r.ORM(), projectID)
}

// GetProjectMCPConfigsJSON reads one column of the project config record: the
// stored scoped MCP configs. It returns sql.ErrNoRows, unwrapped, when the
// project has no record.
func (r *Repo) GetProjectMCPConfigsJSON(ctx context.Context, projectID string) (*string, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return store.GetProjectMCPConfigsJSON(ctx, r.ORM(), projectID)
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
	return store.InsertWorktree(ctx, r.ORM(), worktree)
}

func (r *Repo) GetWorktree(ctx context.Context, id string) (*Worktree, error) {
	if id == "" {
		return nil, fmt.Errorf("worktree ID cannot be empty")
	}
	return store.GetWorktree(ctx, r.ORM(), id)
}

func (r *Repo) GetWorktreeByPath(ctx context.Context, path string) (*Worktree, error) {
	if path == "" {
		return nil, fmt.Errorf("worktree path cannot be empty")
	}
	return store.GetWorktreeByPath(ctx, r.ORM(), path)
}

func (r *Repo) GetWorktreeByIdempotencyKey(ctx context.Context, projectID, key string) (*Worktree, error) {
	// An empty key means the caller opted out of idempotency, not that it is
	// asking about the rows that happen to have no key.
	if projectID == "" || key == "" {
		return nil, nil
	}
	return store.GetWorktreeByIdempotencyKey(ctx, r.ORM(), projectID, key)
}

func (r *Repo) GetLiveWorktreeByName(ctx context.Context, projectID, name string) (*Worktree, error) {
	if projectID == "" || name == "" {
		return nil, nil
	}
	return store.GetLiveWorktreeByName(ctx, r.ORM(), projectID, name)
}

func (r *Repo) ListWorktreesByBranch(ctx context.Context, projectID, branch string) ([]*Worktree, error) {
	if projectID == "" || branch == "" {
		return nil, nil
	}
	return store.ListWorktreesByBranch(ctx, r.ORM(), projectID, branch)
}

func (r *Repo) ListWorktrees(ctx context.Context, filters WorktreeFilters) ([]*Worktree, error) {
	return store.ListWorktrees(ctx, r.ORM(), filters)
}

func (r *Repo) ListWorktreesByIDs(ctx context.Context, ids []string) ([]*Worktree, error) {
	return store.ListWorktreesByIDs(ctx, r.ORM(), ids)
}

func (r *Repo) UpdateWorktree(ctx context.Context, worktree *Worktree) error {
	if worktree == nil {
		return fmt.Errorf("worktree cannot be nil")
	}
	if worktree.ID == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}

	return store.ReplaceWorktree(ctx, r.ORM(), worktree)
}

func (r *Repo) UpdateWorktreeCleanupMetadata(ctx context.Context, id string, metadata *CleanupMetadata) error {
	if id == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}

	return store.SetWorktreeCleanupMetadata(ctx, r.ORM(), id, metadata)
}

func (r *Repo) ListWorktreesForReclaim(ctx context.Context) ([]*core.ReclaimCandidate, error) {
	return store.ListWorktreesForReclaim(ctx, r.ORM())
}

func (r *Repo) ListLiveWorktreePathsForUser(ctx context.Context, userID string) ([]core.WorktreePath, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.ListLiveWorktreePathsForUser(ctx, r.ORM(), userID)
}

// MergeWorktreeCleanupMetadata is a read-modify-write of one row's
// cleanup_metadata under SELECT ... FOR UPDATE.
func (r *Repo) MergeWorktreeCleanupMetadata(ctx context.Context, id string, fn func(m *CleanupMetadata, archived bool) bool) error {
	if id == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}
	return store.MergeWorktreeCleanupMetadata(ctx, r.ORM(), id, fn)
}

func (r *Repo) AdoptWorktreeDaemon(ctx context.Context, id, daemonID string) error {
	if id == "" || daemonID == "" {
		return fmt.Errorf("worktree and daemon IDs cannot be empty")
	}
	return store.AdoptWorktreeDaemon(ctx, r.ORM(), id, daemonID)
}

func (r *Repo) ListHeldWorktreesForUser(ctx context.Context, userID string) ([]*core.HeldWorktree, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.ListHeldWorktreesForUser(ctx, r.ORM(), userID)
}

func (r *Repo) DeleteWorktree(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}
	return store.RemoveWorktree(ctx, r.ORM(), id)
}

func (r *Repo) ArchiveWorktree(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}
	return store.ArchiveWorktree(ctx, r.ORM(), id)
}

func (r *Repo) UnarchiveWorktree(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}
	return store.UnarchiveWorktree(ctx, r.ORM(), id)
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
	return store.InsertRepo(ctx, r.ORM(), repo)
}

func (r *Repo) GetRepo(ctx context.Context, id string) (*core.Repo, error) {
	if id == "" {
		return nil, fmt.Errorf("repo ID cannot be empty")
	}
	return store.GetRepo(ctx, r.ORM(), id)
}

func (r *Repo) GetRepoByProjectAndPath(ctx context.Context, projectID, relativePath string) (*core.Repo, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return store.GetRepoByProjectAndPath(ctx, r.ORM(), projectID, relativePath)
}

func (r *Repo) ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return store.ListReposByProject(ctx, r.ORM(), projectID)
}

func (r *Repo) UpdateRepo(ctx context.Context, repo *core.Repo) error {
	if repo == nil {
		return fmt.Errorf("repo cannot be nil")
	}
	if repo.ID == "" {
		return fmt.Errorf("repo ID cannot be empty")
	}
	return store.ReplaceRepo(ctx, r.ORM(), repo)
}

func (r *Repo) DeleteRepo(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("repo ID cannot be empty")
	}
	return store.RemoveRepo(ctx, r.ORM(), id)
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

	return store.UpsertSetting(ctx, r.ORM(), setting)
}

func (r *Repo) GetSetting(ctx context.Context, userID string, projectID *string, key string) (*Setting, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}
	if key == "" {
		return nil, fmt.Errorf("setting key cannot be empty")
	}

	return store.GetSetting(ctx, r.ORM(), userID, projectID, key)
}

func (r *Repo) ListSettings(ctx context.Context, userID string, projectID *string) ([]*Setting, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}

	return store.ListSettings(ctx, r.ORM(), userID, projectID)
}

func (r *Repo) ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*Setting, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}
	if keyPattern == "" {
		return nil, fmt.Errorf("key pattern cannot be empty")
	}

	return store.ListSettingsByKey(ctx, r.ORM(), userID, keyPattern)
}

func (r *Repo) UpdateSetting(ctx context.Context, setting *Setting) error {
	if setting == nil {
		return fmt.Errorf("setting cannot be nil")
	}
	if setting.ID == "" {
		return fmt.Errorf("setting ID cannot be empty")
	}

	return store.ReplaceSettingValue(ctx, r.ORM(), setting)
}

func (r *Repo) DeleteSetting(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("setting ID cannot be empty")
	}
	return store.RemoveSetting(ctx, r.ORM(), id)
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

func (r *Repo) GetAutomationToken(ctx context.Context, userID, daemonID string) (string, error) {
	if userID == "" || daemonID == "" {
		return "", fmt.Errorf("user_id and daemon_id cannot be empty")
	}
	return r.settings.GetAutomationToken(ctx, userID, daemonID)
}

func (r *Repo) SetAutomationToken(ctx context.Context, userID, daemonID, token string) error {
	if userID == "" || daemonID == "" {
		return fmt.Errorf("user_id and daemon_id cannot be empty")
	}
	return r.settings.SetAutomationToken(ctx, userID, daemonID, token)
}

func (r *Repo) DeleteAutomationToken(ctx context.Context, userID, daemonID string) error {
	if userID == "" || daemonID == "" {
		return fmt.Errorf("user_id and daemon_id cannot be empty")
	}
	return r.settings.DeleteAutomationToken(ctx, userID, daemonID)
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

	return store.GetVisibilityOverride(ctx, r.ORM(), userID, reliantv1.HiddenItemType(itemType), slug)
}

// ListVisibilityOverrides returns all user overrides for a given item type.
func (r *Repo) ListVisibilityOverrides(ctx context.Context, userID string, itemType int32) (map[string]bool, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id cannot be empty")
	}
	if itemType != 1 && itemType != 2 { // HIDDEN_ITEM_TYPE_WORKFLOW=1, HIDDEN_ITEM_TYPE_PRESET=2
		return nil, fmt.Errorf("item_type must be WORKFLOW (1) or PRESET (2)")
	}

	return store.ListVisibilityOverrides(ctx, r.ORM(), userID, reliantv1.HiddenItemType(itemType))
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

	return store.SetVisibilityOverride(ctx, r.ORM(), userID, reliantv1.HiddenItemType(itemType), slug, isVisible)
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

	return store.RemoveVisibilityOverride(ctx, r.ORM(), userID, reliantv1.HiddenItemType(itemType), slug)
}

// GetItemDefault returns the system default visibility for an item.
func (r *Repo) GetItemDefault(ctx context.Context, itemType int32, slug string) (*ItemDefault, error) {
	if itemType != 1 && itemType != 2 { // HIDDEN_ITEM_TYPE_WORKFLOW=1, HIDDEN_ITEM_TYPE_PRESET=2
		return nil, fmt.Errorf("item_type must be WORKFLOW (1) or PRESET (2)")
	}
	if slug == "" {
		return nil, fmt.Errorf("slug cannot be empty")
	}

	return store.GetItemDefault(ctx, r.ORM(), itemType, slug)
}

// ListHiddenItemDefaults returns all items that are hidden by default.
func (r *Repo) ListHiddenItemDefaults(ctx context.Context, itemType int32) ([]string, error) {
	if itemType != 1 && itemType != 2 { // HIDDEN_ITEM_TYPE_WORKFLOW=1, HIDDEN_ITEM_TYPE_PRESET=2
		return nil, fmt.Errorf("item_type must be WORKFLOW (1) or PRESET (2)")
	}

	return store.ListHiddenItemDefaults(ctx, r.ORM(), itemType)
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

	return store.GetDefaultPresetAssignments(ctx, r.ORM(), workflowName)
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

	return store.InsertPlan(ctx, r.ORM(), plan)
}

func (r *Repo) GetPlan(ctx context.Context, id string) (*Plan, error) {
	if id == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}

	return store.GetPlan(ctx, r.ORM(), id)
}

func (r *Repo) GetPlanByThreadID(ctx context.Context, threadID string) (*Plan, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	return store.GetPlanByThreadID(ctx, r.ORM(), threadID)
}

func (r *Repo) ListPlansByThread(ctx context.Context, threadID string) ([]*Plan, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	return store.ListPlansByThread(ctx, r.ORM(), threadID)
}

func (r *Repo) ListPlansByChatID(ctx context.Context, chatID string) ([]*Plan, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return store.ListPlansByChatID(ctx, r.ORM(), chatID)
}

func (r *Repo) ListPlansByProject(ctx context.Context, projectID string) ([]*Plan, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}

	return store.ListPlansByProject(ctx, r.ORM(), projectID)
}

func (r *Repo) UpdatePlan(ctx context.Context, plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("plan cannot be nil")
	}
	if plan.ID == "" {
		return fmt.Errorf("plan ID cannot be empty")
	}

	return store.ReplacePlan(ctx, r.ORM(), plan)
}

func (r *Repo) UpdatePlanStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error {
	if id == "" {
		return fmt.Errorf("plan ID cannot be empty")
	}

	return store.UpdatePlanStatus(ctx, r.ORM(), id, status, completedAt)
}

func (r *Repo) DeletePlan(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("plan ID cannot be empty")
	}
	return store.RemovePlan(ctx, r.ORM(), id)
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

	return store.InsertTask(ctx, r.ORM(), task)
}

func (r *Repo) GetTask(ctx context.Context, id string) (*Task, error) {
	if id == "" {
		return nil, fmt.Errorf("task ID cannot be empty")
	}

	return store.GetTask(ctx, r.ORM(), id)
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

	return store.ListTasksByPlan(ctx, r.ORM(), planID)
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

	return store.ReplaceTask(ctx, r.ORM(), task)
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

	return store.UpdateTaskStatus(ctx, r.ORM(), id, status, completedAt)
}

func (r *Repo) DeleteTask(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("task ID cannot be empty")
	}
	return store.RemoveTask(ctx, r.ORM(), id)
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
	return store.InsertTaskDependency(ctx, r.ORM(), dep)
}

func (r *Repo) GetTaskDependency(ctx context.Context, id string) (*TaskDependency, error) {
	if id == "" {
		return nil, fmt.Errorf("dependency ID cannot be empty")
	}
	return store.GetTaskDependency(ctx, r.ORM(), id)
}

func (r *Repo) ListTaskDependenciesByTask(ctx context.Context, taskID string) ([]*TaskDependency, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task ID cannot be empty")
	}
	return store.ListTaskDependenciesByTask(ctx, r.ORM(), taskID)
}

func (r *Repo) ListBlockersForTask(ctx context.Context, taskID string) ([]*TaskDependency, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task ID cannot be empty")
	}
	return store.ListBlockersForTask(ctx, r.ORM(), taskID)
}

func (r *Repo) ListDependenciesByPlan(ctx context.Context, planID string) ([]*TaskDependency, error) {
	if planID == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}
	return store.ListDependenciesByPlan(ctx, r.ORM(), planID)
}

func (r *Repo) DeleteTaskDependency(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("dependency ID cannot be empty")
	}
	return store.RemoveTaskDependency(ctx, r.ORM(), id)
}

func (r *Repo) DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error {
	if fromTaskID == "" || toTaskID == "" || depType == 0 {
		return fmt.Errorf("from_task_id, to_task_id, and dependency_type are required")
	}
	return store.DeleteTaskDependencyByPair(ctx, r.ORM(), fromTaskID, toTaskID, depType)
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
	return store.InsertBackgroundProcess(ctx, r.ORM(), process)
}

func (r *Repo) GetBackgroundProcess(ctx context.Context, id string) (*BackgroundProcess, error) {
	if id == "" {
		return nil, fmt.Errorf("process ID cannot be empty")
	}
	return store.GetBackgroundProcess(ctx, r.ORM(), id)
}

func (r *Repo) ListBackgroundProcesses(ctx context.Context, filters BackgroundProcessFilters) ([]*BackgroundProcess, error) {
	return store.ListBackgroundProcesses(ctx, r.ORM(), filters)
}

func (r *Repo) UpdateBackgroundProcessStatus(ctx context.Context, id string, status BackgroundProcessStatus, exitCode *int, endedAt *time.Time) error {
	if id == "" {
		return fmt.Errorf("process ID cannot be empty")
	}
	return store.SetBackgroundProcessStatus(ctx, r.ORM(), id, status, exitCode, endedAt)
}

func (r *Repo) UpdateBackgroundProcessPID(ctx context.Context, id string, pid int, signature string) error {
	if id == "" {
		return fmt.Errorf("process ID cannot be empty")
	}
	return store.SetBackgroundProcessPID(ctx, r.ORM(), id, pid, signature)
}

func (r *Repo) GetRunningBackgroundProcesses(ctx context.Context) ([]*BackgroundProcess, error) {
	return store.ListRunningBackgroundProcesses(ctx, r.ORM())
}

func (r *Repo) MarkStaleProcesses(ctx context.Context, processIDs []string) error {
	return store.MarkStaleBackgroundProcesses(ctx, r.ORM(), processIDs)
}

func (r *Repo) CleanupOldBackgroundProcesses(ctx context.Context, olderThan time.Time) (int64, error) {
	return store.DeleteEndedBackgroundProcesses(ctx, r.ORM(), olderThan)
}

// ==================== Background Process Output ====================

func (r *Repo) CreateBackgroundProcessOutputBatch(ctx context.Context, lines []BackgroundProcessOutputLine) error {
	return store.InsertBackgroundProcessOutput(ctx, r.ORM(), lines)
}

func (r *Repo) GetBackgroundProcessOutput(ctx context.Context, processID string, afterSeq int64, limit int) ([]BackgroundProcessOutputLine, error) {
	if processID == "" {
		return nil, fmt.Errorf("process ID cannot be empty")
	}
	if limit <= 0 {
		limit = 10000
	}
	return store.ReadBackgroundProcessOutput(ctx, r.ORM(), processID, afterSeq, limit)
}

// ==================== Attachments ====================

func (r *Repo) CreateAttachment(ctx context.Context, attachment *Attachment) error {
	return store.InsertAttachment(ctx, r.ORM(), attachment)
}

func (r *Repo) GetAttachment(ctx context.Context, id string) (*Attachment, error) {
	return store.GetAttachment(ctx, r.ORM(), id)
}

func (r *Repo) GetAttachmentsByIDs(ctx context.Context, ids []string) ([]*Attachment, error) {
	return store.GetAttachmentsByIDs(ctx, r.ORM(), ids)
}

func (r *Repo) DeleteAttachment(ctx context.Context, id string) error {
	return store.RemoveAttachment(ctx, r.ORM(), id)
}

// ==================== Context Usage ====================

// threadTokenCountAt is the token count the thread's context window at
// contextSeq last reported — its newest message carrying token data, at or
// before maxSeq when set — or 0 when none has any.
func (r *Repo) threadTokenCountAt(ctx context.Context, thread string, contextSeq int, maxSeq *int64) (int64, error) {
	msg, err := store.LatestMessageWithTokens(ctx, r.ORM(), thread, int64(contextSeq), maxSeq)
	if err != nil || msg == nil || msg.TokenCount == nil {
		return 0, err
	}
	return int64(*msg.TokenCount), nil
}

// countMessages counts the messages opts select.
func (r *Repo) countMessages(ctx context.Context, opts ...orm.QueryOption) (int, error) {
	n, err := store.CountMessage(ctx, r.ORM(), opts...)
	if err != nil {
		return 0, fmt.Errorf("failed to count messages: %w", err)
	}
	return int(n), nil
}

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
// - maxSeq: optional - if set, returns tokens at that chat-global seq (for fork points)
//
//	if nil, returns current tokens (most recent message with data)
//
// Returns 0 if no token data exists (caller should estimate if needed).
func (r *Repo) GetThreadTokenCount(ctx context.Context, thread string, maxSeq *int64) (int64, error) {
	if thread == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	// Get the Thread record to find chat_id and fork metadata
	threadRecord, _, err := r.GetThreadWithParent(ctx, thread)
	if err != nil {
		// Thread not found - try querying messages directly using thread as both chat_id and thread
		// This handles legacy data where Thread records may not exist
		logging.DebugContext(ctx, "[GetThreadTokenCount] Thread record not found, trying direct query",
			"thread", thread,
			"error", err)
		return r.getThreadTokenCountDirect(ctx, thread, thread, maxSeq)
	}

	// Get current context sequence for the thread
	contextSeq, err := r.GetMaxContextSequenceInThread(ctx, thread)
	if err != nil {
		logging.WarnContext(ctx, "[GetThreadTokenCount] Error getting context sequence",
			"thread", thread,
			"error", err)
		contextSeq = 0
	}

	// Query for token count at or before maxSeq
	localTokens, err := r.threadTokenCountAt(ctx, thread, contextSeq, maxSeq)
	if err != nil {
		logging.WarnContext(ctx, "[GetThreadTokenCount] Error querying token count",
			"thread", thread,
			"contextSequence", contextSeq,
			"maxSeq", maxSeq,
			"error", err)
		localTokens = 0
	}

	// If we have local token data, return it
	if localTokens > 0 {
		logging.DebugContext(ctx, "[GetThreadTokenCount] Found local token data",
			"thread", thread,
			"tokens", localTokens)
		return localTokens, nil
	}

	// No local token data - check for fork inheritance
	if threadRecord.ParentThreadID == nil || threadRecord.ForkAtMessageID == nil {
		// No fork metadata, return 0
		logging.DebugContext(ctx, "[GetThreadTokenCount] No local tokens and no fork metadata",
			"thread", thread)
		return 0, nil
	}

	parentThreadID := *threadRecord.ParentThreadID

	// Guard against self-referential forks
	if parentThreadID == thread {
		logging.WarnContext(ctx, "[GetThreadTokenCount] Skipping self-referential fork",
			"thread", thread)
		return 0, nil
	}

	forkMsg, err := r.GetMessage(ctx, *threadRecord.ForkAtMessageID)
	if err != nil {
		return 0, fmt.Errorf("failed to get fork message %s: %w", *threadRecord.ForkAtMessageID, err)
	}

	// Recursively get parent's token count at the fork point
	logging.DebugContext(ctx, "[GetThreadTokenCount] Inheriting tokens from parent",
		"thread", thread,
		"parentThread", parentThreadID,
		"forkAtMessageID", *threadRecord.ForkAtMessageID,
		"forkSeq", forkMsg.Seq)

	return r.GetThreadTokenCount(ctx, parentThreadID, &forkMsg.Seq)
}

// getThreadTokenCountDirect queries token count directly using chat_id and thread.
// Used as fallback when Thread record doesn't exist.
func (r *Repo) getThreadTokenCountDirect(ctx context.Context, chatID, thread string, maxSeq *int64) (int64, error) {
	_ = chatID // kept for API compatibility

	contextSeq, err := r.GetMaxContextSequenceInThread(ctx, thread)
	if err != nil {
		contextSeq = 0
	}
	tokens, err := r.threadTokenCountAt(ctx, thread, contextSeq, maxSeq)
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
// no model-bearing message is found. maxSeq bounds inheritance to a fork
// point, the same way maxOrdinal used to -- see GetThreadTokenCount.
func (r *Repo) resolveThreadCompactionThreshold(ctx context.Context, thread string, maxSeq *int64) int {
	if thread == "" {
		return models.UnknownModelCompactionFloor
	}

	contextSeq, err := r.GetMaxContextSequenceInThread(ctx, thread)
	if err != nil {
		contextSeq = 0
	}

	msg, err := r.GetLatestMessageWithTokensInThread(ctx, thread, contextSeq)
	if err == nil && msg != nil && msg.TokenCount != nil && (maxSeq == nil || msg.Seq <= *maxSeq) {
		if msg.Model != nil {
			return models.CompactionThresholdForModel(*msg.Model)
		}
		return models.UnknownModelCompactionFloor
	}

	// No local token-bearing message — follow fork inheritance like GetThreadTokenCount.
	threadRecord, _, err := r.GetThreadWithParent(ctx, thread)
	if err != nil || threadRecord.ParentThreadID == nil || threadRecord.ForkAtMessageID == nil {
		return models.UnknownModelCompactionFloor
	}
	parentThreadID := *threadRecord.ParentThreadID
	if parentThreadID == thread {
		return models.UnknownModelCompactionFloor
	}
	forkMsg, err := r.GetMessage(ctx, *threadRecord.ForkAtMessageID)
	if err != nil {
		return models.UnknownModelCompactionFloor
	}
	return r.resolveThreadCompactionThreshold(ctx, parentThreadID, &forkMsg.Seq)
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

// SetWorkflowOutcome records the run's verdict — the outcome declared by the
// terminal node it reached. Distinct from UpdateWorkflowStatus: status is the
// Temporal-owned lifecycle and gets reconciled back from Temporal, so it cannot
// carry "the work did not pass". No chat-activity emit: the outcome does not
// change whether the chat is busy, only what the finished run means.
func (r *Repo) SetWorkflowOutcome(ctx context.Context, id string, outcome string) error {
	return r.workflows.SetWorkflowOutcome(ctx, id, outcome)
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
	if wf.Status.State == WorkflowStateActive {
		return // Fast path — already active, nothing to do
	}
	if wf.Status.StopReason == StopReasonPaused {
		return // Paused is a deliberate user action — don't override it
	}

	logging.InfoContext(ctx, "[EnsureWorkflowRunning] Workflow not marked active, correcting",
		"workflowID", workflowID,
		"chatID", chatID,
		"currentStatus", wf.Status.Label(),
	)
	if err := r.UpdateWorkflowStatus(ctx, workflowID, Active()); err != nil {
		logging.WarnContext(ctx, "[EnsureWorkflowRunning] Failed to update workflow status",
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

// CascadeTerminalStatusToDescendants moves every descendant workflow record
// owned by a parent to the terminal status the parent itself reached. Called
// when a workflow ends, to drain the children (spawn children, thread records,
// etc.) that are still running or paused.
func (r *Repo) CascadeTerminalStatusToDescendants(ctx context.Context, parentWorkflowID string, reason WorkflowStopReason) error {
	return r.workflows.CascadeTerminalStatusToDescendants(ctx, parentWorkflowID, reason)
}

// ReapOrphanedWorkflowDescendants ends running/paused workflows whose parent is
// already terminal, at the terminal ancestor's status, returning the rows
// repaired. See the Repository interface for why the backstop is needed.
func (r *Repo) ReapOrphanedWorkflowDescendants(ctx context.Context) ([]ReapedWorkflow, error) {
	return r.workflows.ReapOrphanedWorkflowDescendants(ctx)
}

// ReviveSubtreeLiveAt brings a reset-and-replayed run's subtree back to life:
// every descendant workflow row that was live at the reset point, the threads
// those rows own, and the root's own thread. See the Repository interface for
// why a resume has to do this, and internal/store/workflow_repo_ext.go
// (reviveSubtreeLiveAtSQL) for the predicate and for why both halves are a
// single atomic statement.
func (r *Repo) ReviveSubtreeLiveAt(ctx context.Context, rootWorkflowID string, at time.Time) (workflowsRevived, threadsRevived int64, err error) {
	if rootWorkflowID == "" {
		return 0, 0, fmt.Errorf("root workflow ID cannot be empty")
	}
	return r.workflows.ReviveSubtreeLiveAt(ctx, rootWorkflowID, at)
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

// ==================== Triggers ====================
//
// Every method here delegates to internal/store over r.ORM(), which joins the
// transaction ctx carries — so they pick up an ambient RunTx transaction
// automatically. That is what lets the launcher write the trigger event row in
// the SAME transaction as the chat it launches: either both commit or neither
// does, and there is no window where a chat exists with nothing recording why.

func (r *Repo) CreateTrigger(ctx context.Context, t *core.Trigger) error {
	if t == nil {
		return fmt.Errorf("trigger cannot be nil")
	}
	return store.InsertTrigger(ctx, r.ORM(), t)
}

func (r *Repo) GetTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	if id == "" {
		return nil, fmt.Errorf("trigger ID cannot be empty")
	}
	return store.GetTrigger(ctx, r.ORM(), id)
}

func (r *Repo) ListTriggers(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error) {
	if f.UserID == "" {
		return nil, fmt.Errorf("user ID cannot be empty; use ListAllTriggers to list every user's triggers")
	}
	return store.ListTriggersForUser(ctx, r.ORM(), f)
}

func (r *Repo) ListAllTriggers(ctx context.Context) ([]*core.Trigger, error) {
	return store.ListAllTriggers(ctx, r.ORM())
}

func (r *Repo) LockTrigger(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("trigger ID cannot be empty")
	}
	return store.LockTrigger(ctx, r.ORM(), id)
}

func (r *Repo) UpdateTrigger(ctx context.Context, t *core.Trigger) error {
	if t == nil {
		return fmt.Errorf("trigger cannot be nil")
	}
	if t.ID == "" {
		return fmt.Errorf("trigger ID cannot be empty")
	}
	return store.ReplaceTrigger(ctx, r.ORM(), t)
}

func (r *Repo) DeleteTrigger(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("trigger ID cannot be empty")
	}
	return store.RemoveTrigger(ctx, r.ORM(), id)
}

func (r *Repo) SetTriggerEnabled(ctx context.Context, id string, enabled bool) error {
	if id == "" {
		return fmt.Errorf("trigger ID cannot be empty")
	}
	return store.SetTriggerEnabled(ctx, r.ORM(), id, enabled)
}

func (r *Repo) SetTriggerProjection(ctx context.Context, id string, p core.TriggerProjection) error {
	if id == "" {
		return fmt.Errorf("trigger ID cannot be empty")
	}
	return store.SetTriggerProjection(ctx, r.ORM(), id, p)
}

func (r *Repo) ListAllWorkflowTriggerActivations(ctx context.Context) ([]*core.Trigger, error) {
	return store.ListAllWorkflowTriggerActivations(ctx, r.ORM())
}

func (r *Repo) CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (bool, error) {
	if ev == nil {
		return false, fmt.Errorf("trigger event cannot be nil")
	}
	if ev.DedupeKey == "" {
		// An empty dedupe key would make every firing of this kind collide
		// with every other one, which is far worse than a missing row: the
		// second real firing would silently report "already launched".
		return false, fmt.Errorf("trigger event dedupe key cannot be empty")
	}
	return store.InsertTriggerEventIfAbsent(ctx, r.ORM(), ev)
}

func (r *Repo) GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error) {
	if dedupeKey == "" {
		return nil, fmt.Errorf("trigger event dedupe key cannot be empty")
	}
	return store.GetTriggerEventByDedupe(ctx, r.ORM(), kind, dedupeKey)
}

func (r *Repo) GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return store.GetTriggerEventByChatID(ctx, r.ORM(), chatID)
}

func (r *Repo) GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return store.GetTriggerEventByChat(ctx, r.ORM(), kind, chatID)
}

func (r *Repo) CountLiveLaunchedRuns(ctx context.Context, userID string, kind core.TriggerEventKind) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("user ID cannot be empty")
	}
	return store.CountLiveLaunchedRuns(ctx, r.ORM(), userID, kind)
}

func (r *Repo) UpdateTriggerEventOutcome(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string, chatID *string) error {
	if id == "" {
		return fmt.Errorf("trigger event ID cannot be empty")
	}
	return store.UpdateTriggerEventOutcome(ctx, r.ORM(), id, outcome, detail, chatID)
}

func (r *Repo) SetLaunchEventRunStatus(ctx context.Context, chatID, status string) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return store.SetLaunchEventRunStatus(ctx, r.ORM(), chatID, status)
}

func (r *Repo) UpdateTriggerEventPayload(ctx context.Context, id string, payload map[string]any) error {
	if id == "" {
		return fmt.Errorf("trigger event ID cannot be empty")
	}
	return store.UpdateTriggerEventPayload(ctx, r.ORM(), id, payload)
}

func (r *Repo) ListTriggerEvents(ctx context.Context, f core.TriggerEventFilters) ([]*core.TriggerEventWithRun, bool, error) {
	if f.TriggerID == "" {
		return nil, false, fmt.Errorf("trigger ID cannot be empty")
	}
	if f.UserID == "" {
		return nil, false, fmt.Errorf("user ID cannot be empty")
	}
	return store.ListTriggerEventsPage(ctx, r.ORM(), f)
}

func (r *Repo) ListInboxPending(ctx context.Context, userID string) ([]*core.InboxPending, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return r.inbox.ListInboxPending(ctx, userID)
}

func (r *Repo) ListDismissedInboxItemIDs(ctx context.Context, userID string, itemIDs []string) (map[string]bool, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return r.inbox.ListDismissedInboxItemIDs(ctx, userID, itemIDs)
}

func (r *Repo) DismissInboxItems(ctx context.Context, userID string, itemIDs []string, at time.Time) error {
	if userID == "" {
		return fmt.Errorf("user ID cannot be empty")
	}
	return r.inbox.DismissInboxItems(ctx, userID, itemIDs, at)
}

func (r *Repo) RestoreInboxItems(ctx context.Context, userID string, itemIDs []string) error {
	if userID == "" {
		return fmt.Errorf("user ID cannot be empty")
	}
	return r.inbox.RestoreInboxItems(ctx, userID, itemIDs)
}

func (r *Repo) CreateRunEvent(ctx context.Context, ev *core.RunEvent) (bool, error) {
	if ev == nil {
		return false, fmt.Errorf("run event cannot be nil")
	}
	if ev.DedupeKey == "" {
		// An empty key would collide every transition with every other one,
		// and the second real event would silently read as a retry.
		return false, fmt.Errorf("run event dedupe key cannot be empty")
	}
	return r.runEvents.CreateRunEvent(ctx, ev)
}

func (r *Repo) GetRunEvent(ctx context.Context, id string) (*core.RunEvent, error) {
	if id == "" {
		return nil, fmt.Errorf("run event ID cannot be empty")
	}
	return r.runEvents.GetRunEvent(ctx, id)
}

func (r *Repo) GetRunEventByDedupe(ctx context.Context, dedupeKey string) (*core.RunEvent, error) {
	if dedupeKey == "" {
		return nil, fmt.Errorf("run event dedupe key cannot be empty")
	}
	return r.runEvents.GetRunEventByDedupe(ctx, dedupeKey)
}

func (r *Repo) HasEnabledTriggerOfKind(ctx context.Context, userID string, kind core.TriggerKind) (bool, error) {
	if userID == "" {
		return false, fmt.Errorf("user ID cannot be empty")
	}
	return r.runEvents.HasEnabledTriggerOfKind(ctx, userID, kind)
}

func (r *Repo) LockChatForRunEvent(ctx context.Context, chatID string) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return r.runEvents.LockChatForRunEvent(ctx, chatID)
}

func (r *Repo) CountOtherPendingBlockers(ctx context.Context, chatID, excludeID string) (int, error) {
	if chatID == "" {
		return 0, fmt.Errorf("chat ID cannot be empty")
	}
	return r.runEvents.CountOtherPendingBlockers(ctx, chatID, excludeID)
}

func (r *Repo) ClaimRunEvents(ctx context.Context, now, leaseUntil time.Time, max int) ([]*core.RunEvent, error) {
	if max <= 0 {
		return nil, nil
	}
	return r.runEvents.ClaimRunEvents(ctx, now, leaseUntil, max)
}

func (r *Repo) MarkRunEventDispatched(ctx context.Context, id string, at time.Time) error {
	if id == "" {
		return fmt.Errorf("run event ID cannot be empty")
	}
	return r.runEvents.MarkRunEventDispatched(ctx, id, at)
}

func (r *Repo) DeleteDispatchedRunEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return r.runEvents.DeleteDispatchedRunEventsBefore(ctx, cutoff)
}

func (r *Repo) RecentTriggerFirings(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.RecentTriggerFirings(ctx, r.ORM(), userID, triggerIDs, perTrigger)
}

func (r *Repo) FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.FiringsSinceLastSuccess(ctx, r.ORM(), userID, triggerIDs, perTrigger)
}

func (r *Repo) GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error) {
	if triggerID == "" {
		return nil, fmt.Errorf("trigger ID cannot be empty")
	}
	return store.GetLatestTriggerEvent(ctx, r.ORM(), triggerID, outcome)
}

func (r *Repo) SetTriggerWebhookTokenHash(ctx context.Context, id string, hash []byte) error {
	if id == "" {
		return fmt.Errorf("trigger ID cannot be empty")
	}
	return store.SetTriggerWebhookTokenHash(ctx, r.ORM(), id, hash)
}

func (r *Repo) SetTriggerWebhookSecret(ctx context.Context, id string, sealed []byte) error {
	if id == "" {
		return fmt.Errorf("trigger ID cannot be empty")
	}
	return store.SetTriggerWebhookSecret(ctx, r.ORM(), id, sealed)
}

func (r *Repo) GetTriggerWebhookCredentials(ctx context.Context, id string) (*core.TriggerWebhookCredentials, error) {
	if id == "" {
		return nil, core.ErrTriggerNotFound
	}
	return store.GetTriggerWebhookCredentials(ctx, r.ORM(), id)
}

func (r *Repo) ListIntegrationTriggers(ctx context.Context, integration string) ([]*core.IntegrationTriggerRoute, error) {
	return store.ListIntegrationTriggers(ctx, r.ORM(), integration)
}

func (r *Repo) ListStalePendingTriggerEvents(ctx context.Context, olderThan time.Time, limit int) ([]*core.TriggerEvent, error) {
	return store.ListStalePendingTriggerEvents(ctx, r.ORM(), olderThan, limit)
}

func (r *Repo) ClaimPendingTriggerEvent(ctx context.Context, id string, payload map[string]any) (bool, error) {
	return store.ClaimPendingTriggerEvent(ctx, r.ORM(), id, payload)
}

func (r *Repo) SettlePendingTriggerEvent(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string) (bool, error) {
	return store.SettlePendingTriggerEvent(ctx, r.ORM(), id, outcome, detail)
}

func (r *Repo) GetTriggerRegistration(ctx context.Context, triggerID string) (*core.TriggerRegistration, error) {
	return store.GetTriggerRegistration(ctx, r.ORM(), triggerID)
}

func (r *Repo) ListTriggerRegistrations(ctx context.Context, userID string, triggerIDs []string) (map[string]*core.TriggerRegistration, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.ListTriggerRegistrationsForUser(ctx, r.ORM(), userID, triggerIDs)
}

func (r *Repo) UpsertTriggerRegistration(ctx context.Context, reg *core.TriggerRegistration) error {
	if reg == nil || reg.TriggerID == "" {
		return fmt.Errorf("trigger registration needs a trigger ID")
	}
	return store.UpsertTriggerRegistration(ctx, r.ORM(), reg)
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

func (r *Repo) GetBasicStepExecutionsForChat(ctx context.Context, chatID string) ([]*ChatStepExecution, error) {
	return r.workflows.GetBasicStepExecutionsForChat(ctx, chatID)
}

func (r *Repo) GetStepExecutionsForChat(ctx context.Context, chatID string) ([]*ChatStepExecution, error) {
	return r.workflows.GetStepExecutionsForChat(ctx, chatID)
}

func (r *Repo) ListStepExecutionRecordsForChat(ctx context.Context, chatID, nodePath string, limit int) ([]*StepExecution, error) {
	return r.workflows.ListStepExecutionRecordsForChat(ctx, chatID, nodePath, limit)
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
	if thread.ChatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
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

func (r *Repo) GetRootThread(ctx context.Context, chatID string) (*Thread, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return r.threads.GetRootThread(ctx, chatID)
}

func (r *Repo) GetThreadWithParent(ctx context.Context, id string) (*Thread, *string, error) {
	if id == "" {
		return nil, nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.threads.GetThreadWithParent(ctx, id)
}

func (r *Repo) ListThreadsByConversation(ctx context.Context, chatID string) ([]*Thread, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return r.threads.ListThreadsByConversation(ctx, chatID)
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

func (r *Repo) UpdateThreadForkPoint(ctx context.Context, threadID string, forkAtMessageID *string) (*Thread, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.threads.UpdateThreadForkPoint(ctx, threadID, forkAtMessageID)
}

func (r *Repo) UpdateThreadStatus(ctx context.Context, threadID string, status int32, completedAt *time.Time) (*Thread, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return r.threads.UpdateThreadStatus(ctx, threadID, status, completedAt)
}

// ReviveThread moves a terminal thread back to running because a new run has
// started on it. See the Repository interface for why a reused main thread
// needs the inverse of the terminal cascade.
func (r *Repo) ReviveThread(ctx context.Context, threadID string) (int64, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}
	return r.threads.ReviveThread(ctx, threadID)
}

// CascadeTerminalStatusToThreadSubtree closes every thread owned by
// workflowID or any of its descendants to the terminal status the workflow
// subtree just reached. See the Repository interface for why this exists
// alongside CascadeTerminalStatusToDescendants. Takes the same stop reason
// those callers pass to CascadeTerminalStatusToDescendants — the two calls are
// meant to travel together at every site.
//
// A non-terminal reason (PAUSED) is a no-op: a parked run's threads must keep
// reading as live, or message delivery into them starts being refused.
func (r *Repo) CascadeTerminalStatusToThreadSubtree(ctx context.Context, workflowID string, reason WorkflowStopReason) error {
	if workflowID == "" {
		return fmt.Errorf("workflow ID cannot be empty")
	}
	threadStatus, terminal := core.ThreadStatusForStopReason(reason)
	if !terminal {
		return nil
	}
	return r.threads.CascadeTerminalStatusToThreadSubtree(ctx, workflowID, threadStatus)
}

// ReapOrphanedThreads ends running/paused threads whose workflow is already
// terminal, at the workflow's own status, returning the rows repaired. See
// the Repository interface for why the backstop is needed.
func (r *Repo) ReapOrphanedThreads(ctx context.Context) ([]ReapedThread, error) {
	return r.threads.ReapOrphanedThreads(ctx)
}

func (r *Repo) ListThreadsByOrigin(ctx context.Context, conversationID string, origin ThreadOrigin) ([]*Thread, error) {
	if conversationID == "" {
		return nil, fmt.Errorf("conversation ID cannot be empty")
	}
	return r.threads.ListThreadsByOrigin(ctx, conversationID, origin)
}

func (r *Repo) DeleteThread(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("thread ID cannot be empty")
	}
	return r.threads.DeleteThread(ctx, id)
}

func (r *Repo) DeleteThreadsByConversation(ctx context.Context, chatID string) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return r.threads.DeleteThreadsByConversation(ctx, chatID)
}

func (r *Repo) CountThreadsInConversation(ctx context.Context, chatID string) (int64, error) {
	if chatID == "" {
		return 0, fmt.Errorf("chat ID cannot be empty")
	}
	return r.threads.CountThreadsInConversation(ctx, chatID)
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

func (r *Repo) ListForkedThreadIDs(ctx context.Context, threadIDs []string) ([]string, error) {
	if len(threadIDs) == 0 {
		return nil, nil
	}
	return r.contextWindows.ListForkedThreadIDs(ctx, threadIDs)
}

func (r *Repo) GetContextWindowWithThread(ctx context.Context, id string) (*ContextWindow, string, *string, *string, error) {
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

// ErrWorkflowSlugTaken reports that the owner already has a workflow with the
// slug. It is deliberately not retryable: the conflict is permanent, not a race.
var ErrWorkflowSlugTaken = errors.New("workflow slug already taken")

// workflowDraftTxOptions: each draft write is one single-row write plus the
// user_updates insert, which allocates from the per-user counter row and so
// needs READ COMMITTED (see CreateUserUpdate). Under SERIALIZABLE it would
// abort against any concurrent update for the same user.
var workflowDraftTxOptions = TxOptions{Isolation: IsolationReadCommitted}

func (r *Repo) runWorkflowDraftTx(ctx context.Context, f func(txCtx context.Context) error) error {
	return r.RunTxWithOptions(ctx, workflowDraftTxOptions, f)
}

// mapWorkflowSlugConflict turns a unique violation on workflow_drafts into
// ErrWorkflowSlugTaken. The PgError is not wrapped (%w) so isRetryableError
// does not see 23505 and walk the retry ladder for a permanent conflict.
func mapWorkflowSlugConflict(err error, slug string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "workflow_drafts_user_id_slug_key" {
		return fmt.Errorf("%w: %q", ErrWorkflowSlugTaken, slug)
	}
	return err
}

func (r *Repo) CreateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error {
	if draft == nil {
		return fmt.Errorf("draft cannot be nil")
	}
	return r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		if err := r.workflowCatalog.CreateWorkflowDraft(txCtx, draft); err != nil {
			return mapWorkflowSlugConflict(err, draft.Slug)
		}
		return r.publishWorkflowDraftUpdatedByID(txCtx, draft.ID)
	})
}

func (r *Repo) UpsertWorkflowDraft(ctx context.Context, draft *WorkflowDraft) (*WorkflowDraft, error) {
	if draft == nil {
		return nil, fmt.Errorf("draft cannot be nil")
	}
	var saved *WorkflowDraft
	err := r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		row, err := r.workflowCatalog.UpsertWorkflowDraft(txCtx, draft)
		if err != nil {
			return mapWorkflowSlugConflict(err, draft.Slug)
		}
		saved = row
		return r.publishWorkflowDraftUpdated(txCtx, row, false)
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// publishWorkflowDraftUpdated announces a draft write to the owner's open
// editors. Every draft writer funnels through the Repo methods that call this,
// so no tool or RPC can forget. It must run inside the write's transaction: a
// rolled-back write then never announces itself. deleted marks the draft's
// removal (data_json {"deleted": true}); the version is then the last one.
func (r *Repo) publishWorkflowDraftUpdated(ctx context.Context, draft *WorkflowDraft, deleted bool) error {
	if draft == nil {
		return nil
	}
	payload := map[string]any{
		"draft_id": draft.ID,
		"slug":     draft.Slug,
		"version":  draft.Version,
	}
	if deleted {
		payload["deleted"] = true
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal workflow draft update: %w", err)
	}
	return r.CreateUserUpdate(ctx, &UserUpdate{
		UserID:     draft.UserID,
		UpdateType: UserUpdateWorkflowDraftUpdated,
		EntityType: EntityTypeWorkflowDraft,
		EntityID:   draft.ID,
		Data:       data,
	})
}

func (r *Repo) publishWorkflowDraftUpdatedByID(ctx context.Context, id string) error {
	row, err := r.workflowCatalog.GetWorkflowDraft(ctx, id)
	if err != nil {
		return err
	}
	return r.publishWorkflowDraftUpdated(ctx, row, false)
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

func (r *Repo) GetWorkflowDraftBySourcePath(ctx context.Context, userID, sourcePath string) (*WorkflowDraft, error) {
	return r.workflowCatalog.GetWorkflowDraftBySourcePath(ctx, userID, sourcePath)
}

// GetUsableWorkflowBySlug returns the runnable (complete, visible) workflow
// with this slug, or (nil, nil) when there is none. A visible DRAFT with the
// slug is reported as *WorkflowDraftNotRunnableError rather than hidden behind
// "not found": the workflow exists, and the fix is to mark it complete.
func (r *Repo) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error) {
	usable, err := r.workflowCatalog.GetUsableWorkflowBySlug(ctx, userID, slug)
	if err != nil || usable != nil {
		return usable, err
	}
	draft, err := r.workflowCatalog.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return nil, err
	}
	if draft != nil && !draft.IsHidden && draft.Status != WorkflowDraftStatusComplete {
		return nil, &WorkflowDraftNotRunnableError{Slug: slug}
	}
	return nil, nil
}

func (r *Repo) ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*WorkflowDraft, error) {
	return r.workflowCatalog.ListWorkflowDraftsByUser(ctx, userID)
}

func (r *Repo) UpdateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error {
	if draft == nil {
		return fmt.Errorf("draft cannot be nil")
	}
	return r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		if err := r.workflowCatalog.UpdateWorkflowDraft(txCtx, draft); err != nil {
			return mapWorkflowSlugConflict(err, draft.Slug)
		}
		return r.publishWorkflowDraftUpdatedByID(txCtx, draft.ID)
	})
}

func (r *Repo) UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, status WorkflowDraftStatus) error {
	return r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		if err := r.workflowCatalog.UpdateWorkflowDraftDefinition(txCtx, id, name, slug, definition, status); err != nil {
			return mapWorkflowSlugConflict(err, slug)
		}
		return r.publishWorkflowDraftUpdatedByID(txCtx, id)
	})
}

func (r *Repo) SetWorkflowDraftStatus(ctx context.Context, id string, status WorkflowDraftStatus) (*WorkflowDraft, error) {
	var saved *WorkflowDraft
	err := r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		row, err := r.workflowCatalog.SetWorkflowDraftStatus(txCtx, id, status)
		if err != nil {
			return err
		}
		saved = row
		return r.publishWorkflowDraftUpdated(txCtx, row, false)
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

func (r *Repo) SetWorkflowDraftHidden(ctx context.Context, id string, isHidden bool) (*WorkflowDraft, error) {
	var saved *WorkflowDraft
	err := r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		row, err := r.workflowCatalog.SetWorkflowDraftHidden(txCtx, id, isHidden)
		if err != nil {
			return err
		}
		saved = row
		return r.publishWorkflowDraftUpdated(txCtx, row, false)
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// DeleteWorkflowDraft publishes {"deleted": true} with the deleted draft's
// id/slug/last version, built from DELETE ... RETURNING so the read and the
// delete are one atomic statement. Deleting a missing draft is a silent no-op.
func (r *Repo) DeleteWorkflowDraft(ctx context.Context, id string) error {
	return r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		deleted, err := r.workflowCatalog.DeleteWorkflowDraft(txCtx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return r.publishWorkflowDraftUpdated(txCtx, deleted, true)
	})
}

func (r *Repo) DeleteWorkflowDraftBySlug(ctx context.Context, userID, slug string) error {
	return r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		deleted, err := r.workflowCatalog.DeleteWorkflowDraftBySlug(txCtx, userID, slug)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return r.publishWorkflowDraftUpdated(txCtx, deleted, true)
	})
}

func (r *Repo) WorkflowSlugExists(ctx context.Context, userID, slug string) (bool, error) {
	return r.workflowCatalog.WorkflowSlugExists(ctx, userID, slug)
}

func (r *Repo) CountWorkflowDraftsByUser(ctx context.Context, userID string) (int64, error) {
	return r.workflowCatalog.CountWorkflowDraftsByUser(ctx, userID)
}

func (r *Repo) UpdateWorkflowForkedFrom(ctx context.Context, draftID string, forkedFrom string) (*WorkflowDraft, error) {
	var saved *WorkflowDraft
	err := r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		row, err := r.workflowCatalog.UpdateWorkflowForkedFrom(txCtx, draftID, forkedFrom)
		if err != nil {
			return err
		}
		saved = row
		return r.publishWorkflowDraftUpdated(txCtx, row, false)
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// ==================== Preset Repository ====================

func (r *Repo) CreatePreset(ctx context.Context, preset *Preset) (*Preset, error) {
	return store.InsertPreset(ctx, r.ORM(), preset)
}

func (r *Repo) UpsertPreset(ctx context.Context, preset *Preset) (*Preset, error) {
	return store.UpsertPreset(ctx, r.ORM(), preset)
}

func (r *Repo) GetPreset(ctx context.Context, id string) (*Preset, error) {
	return store.GetPreset(ctx, r.ORM(), id)
}

func (r *Repo) GetPresetBySlug(ctx context.Context, userID, slug string) (*Preset, error) {
	return store.GetPresetBySlug(ctx, r.ORM(), userID, slug)
}

func (r *Repo) GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*Preset, error) {
	return store.GetPresetBySlugAndProject(ctx, r.ORM(), userID, slug, projectID)
}

func (r *Repo) ListUserPresets(ctx context.Context, userID string) ([]*Preset, error) {
	return store.ListUserPresets(ctx, r.ORM(), userID)
}

func (r *Repo) ListUserPresetsGlobal(ctx context.Context, userID string) ([]*Preset, error) {
	return store.ListUserPresetsGlobal(ctx, r.ORM(), userID)
}

func (r *Repo) ListUserPresetsByProject(ctx context.Context, userID, projectID string) ([]*Preset, error) {
	return store.ListUserPresetsByProject(ctx, r.ORM(), userID, projectID)
}

func (r *Repo) ListPresetsByTag(ctx context.Context, userID, tag, projectID string) ([]*Preset, error) {
	return store.ListPresetsByTag(ctx, r.ORM(), userID, tag, projectID)
}

func (r *Repo) UpdatePreset(ctx context.Context, preset *Preset) (*Preset, error) {
	return store.ReplacePreset(ctx, r.ORM(), preset)
}

func (r *Repo) DeletePreset(ctx context.Context, id string) error {
	return store.RemovePreset(ctx, r.ORM(), id)
}

func (r *Repo) DeletePresetBySlug(ctx context.Context, userID, slug string) error {
	return store.DeletePresetBySlug(ctx, r.ORM(), userID, slug)
}

func (r *Repo) DeletePresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) error {
	return store.DeletePresetBySlugAndProject(ctx, r.ORM(), userID, slug, projectID)
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
	return store.ListCommandFavorites(ctx, r.ORM(), userID, projectID)
}

func (r *Repo) AddCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error {
	return store.AddCommandFavorite(ctx, r.ORM(), userID, projectID, commandKey)
}

func (r *Repo) RemoveCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error {
	return store.RemoveCommandFavorite(ctx, r.ORM(), userID, projectID, commandKey)
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

// apiKeySealing is the optional sealing surface of the api_keys store.
type apiKeySealing interface {
	SetSealer(postgresstore.CredentialSealer)
	BackfillAPIKeys(ctx context.Context, batch int) (int, error)
	CountUnsealedAPIKeys(ctx context.Context) (int64, error)
	ValidateAPIKeysSealedConstraint(ctx context.Context) error
}

// EnableCredentialSealing gives the settings store its vault; without one,
// provider API keys and provider sign-in tokens (Claude, Codex, Copilot,
// Antigravity) cannot be read or written. Call once at boot, before serving.
func (r *Repo) EnableCredentialSealing(sealer postgresstore.CredentialSealer) error {
	s, ok := r.settings.(apiKeySealing)
	if !ok {
		return fmt.Errorf("settings store does not support api key sealing")
	}
	s.SetSealer(sealer)
	r.accounts.SetSealer(sealer)
	return nil
}

// BackfillAPIKeys seals legacy plaintext api_keys rows. Idempotent; returns the
// number of rows sealed. Requires EnableCredentialSealing.
func (r *Repo) BackfillAPIKeys(ctx context.Context, batch int) (int, error) {
	s, ok := r.settings.(apiKeySealing)
	if !ok {
		return 0, fmt.Errorf("settings store does not support api key sealing")
	}
	return s.BackfillAPIKeys(ctx, batch)
}

// CountUnsealedAPIKeys returns count(api_keys WHERE api_key_sealed IS NULL).
func (r *Repo) CountUnsealedAPIKeys(ctx context.Context) (int64, error) {
	s, ok := r.settings.(apiKeySealing)
	if !ok {
		return 0, fmt.Errorf("settings store does not support api key sealing")
	}
	return s.CountUnsealedAPIKeys(ctx)
}

// ValidateAPIKeysSealedConstraint validates the api_key_sealed NOT NULL check.
func (r *Repo) ValidateAPIKeysSealedConstraint(ctx context.Context) error {
	s, ok := r.settings.(apiKeySealing)
	if !ok {
		return fmt.Errorf("settings store does not support api key sealing")
	}
	return s.ValidateAPIKeysSealedConstraint(ctx)
}

// ListUserIDsWithProviderAccount returns every user holding an account for provider, without
// opening (decrypting) any of them.
func (r *Repo) ListUserIDsWithProviderAccount(ctx context.Context, provider string) ([]string, error) {
	return r.accounts.ListUserIDsWithProviderAccount(ctx, provider)
}
