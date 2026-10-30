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
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ==================== Chats ====================

func (r *Repo) CreateChat(ctx context.Context, chat *Chat) error {
	if chat == nil {
		return fmt.Errorf("chat cannot be nil")
	}
	return store.InsertChat(ctx, r.ORM(), chat)
}

func (r *Repo) GetChat(ctx context.Context, id string) (*Chat, error) {
	if id == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return store.GetChat(ctx, r.ORM(), id)
}

// GetChatRow reads a chat's own row from the chats table — no activity view —
// for a caller that needs none of the activity-derived fields (ChatRow).
func (r *Repo) GetChatRow(ctx context.Context, id string) (*ChatRow, error) {
	if id == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return store.GetChatRow(ctx, r.ORM(), id)
}

// GetChatRowWithUserCheck is an ownership check that reads only the chats
// row: userID's chat, or core.ErrChatNotFound for a chat that is missing or
// another user's. GetChatWithUserCheck is the same check over the activity
// view, for a caller that needs a derived field.
func (r *Repo) GetChatRowWithUserCheck(ctx context.Context, id, userID string) (*ChatRow, error) {
	if id == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.GetChatRowWithUserCheck(ctx, r.ORM(), id, userID)
}

func (r *Repo) GetChatWithUserCheck(ctx context.Context, id string, userID string) (*Chat, error) {
	if id == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}

	return store.GetChatWithUserCheck(ctx, r.ORM(), id, userID)
}

// GetLiveChatIDForWorktree returns a non-archived chat bound to this worktree,
// or an empty string when it is unused.
func (r *Repo) GetLiveChatIDForWorktree(ctx context.Context, worktreeID string) (string, error) {
	return store.GetLiveChatIDForWorktree(ctx, r.ORM(), worktreeID)
}

func (r *Repo) ListChats(ctx context.Context, filters ChatFilters) ([]*Chat, error) {
	return store.ListChats(ctx, r.ORM(), filters)
}

func (r *Repo) SearchChats(ctx context.Context, filters ChatSearchFilters) ([]*Chat, error) {
	return store.SearchChats(ctx, r.ORM(), filters)
}

func (r *Repo) UpdateChat(ctx context.Context, chat *Chat) error {
	if chat == nil {
		return fmt.Errorf("chat cannot be nil")
	}
	if chat.ID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return store.SaveChat(ctx, r.ORM(), chat)
}

func (r *Repo) DeleteChat(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return store.RemoveChat(ctx, r.ORM(), id)
}

func (r *Repo) ListArchivedChats(ctx context.Context, userID string) ([]*ArchivedChatInfo, error) {
	return store.ListArchivedChats(ctx, r.ORM(), userID)
}

// ListRuns is the cross-cutting run list, scoped to filters.UserID.
func (r *Repo) ListRuns(ctx context.Context, filters RunListFilters) ([]*RunListItem, bool, error) {
	return store.ListRuns(ctx, r.ORM(), filters)
}

// ListUnlaunchedTriggerEvents is the run log's other half: the trigger
// events that started no run, under the same filters and keyset as ListRuns.
func (r *Repo) ListUnlaunchedTriggerEvents(ctx context.Context, filters RunListFilters) ([]*UnlaunchedTriggerEvent, bool, error) {
	return store.ListUnlaunchedTriggerEvents(ctx, r.ORM(), filters)
}

func (r *Repo) LastRunPerWorkflow(ctx context.Context, filters RunListFilters) ([]*RunListItem, error) {
	return store.LastRunPerWorkflow(ctx, r.ORM(), filters)
}

// ==================== Messages ====================

// CreateMessage creates a message record in the database.
//
// WARNING: Prefer SaveMessageToThread for most use cases outside workflow activities,
// and CreateNumberedMessage for a message you build yourself. This low-level
// function writes the Ordinal and Seq you set, which must come from
// AllocateMessageOrder (never from the highest number already written); it
// raises the counters to them, so a counter is never behind a row.
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
	// One transaction: the insert raises the ordinal and seq counters to the
	// row's numbers, and the two must not land apart.
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return store.InsertMessage(ctx, r.ORM(), msg)
	})
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
	var created bool
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		created, err = store.InsertMessageOnce(ctx, r.ORM(), msg)
		return err
	})
	return created, err
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
// displayStyle is optional - if provided, sets the UI display style (info/warning/success/hidden).
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
	err := r.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		// The transaction is retried on a serialization conflict, and a retry
		// starts over.
		savedMsg, created = nil, true

		// The thread's ordinal and the chat's seq come from
		// message_order_counters, never from the highest number already
		// written (store.AllocateMessageOrder). When an id already taken makes
		// a once-save write nothing, the numbers it took are a gap.
		ordinal, seq, err := store.AllocateMessageOrder(txCtx, r.ORM(), thread, chatID)
		if err != nil {
			return err
		}

		// Get the current context_sequence for the thread
		// This ensures user messages are saved at the same context_sequence as other messages
		// (important after compaction when context_sequence > 0)
		contextSequence, err := r.getMaxContextSequenceInThread(txCtx, thread)
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
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("message"), id)
	}
	return msg, nil
}

// AllocateMessageOrder takes the next ordinal of threadID and the next seq of
// chatID from their counters (store.AllocateMessageOrder), for a message the
// caller writes with CreateMessage. In the caller's transaction the numbers
// follow commit order and a rollback returns them; outside one they are spent
// as soon as they are taken.
func (r *Repo) AllocateMessageOrder(ctx context.Context, threadID, chatID string) (ordinal, seq int64, err error) {
	if threadID == "" {
		return 0, 0, fmt.Errorf("thread ID cannot be empty")
	}
	if chatID == "" {
		return 0, 0, fmt.Errorf("chat ID cannot be empty")
	}
	err = r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		ordinal, seq, err = store.AllocateMessageOrder(ctx, r.ORM(), threadID, chatID)
		return err
	})
	return ordinal, seq, err
}

// CreateNumberedMessage writes msg as the next message of its thread and chat:
// it allocates msg.Ordinal and msg.Seq (AllocateMessageOrder) and inserts the
// row in one transaction, joining the caller's when ctx carries one. It is
// how a writer outside SaveMessageAtomic and SaveMessageToThread (a repair, a
// denial) appends a message it builds itself.
func (r *Repo) CreateNumberedMessage(ctx context.Context, msg *Message) error {
	if msg == nil {
		return fmt.Errorf("message cannot be nil")
	}
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		ordinal, seq, err := r.AllocateMessageOrder(ctx, msg.ThreadID, msg.ChatID)
		if err != nil {
			return err
		}
		msg.Ordinal, msg.Seq = ordinal, seq
		return r.CreateMessage(ctx, msg)
	})
}

func (r *Repo) ListMessages(ctx context.Context, chatID string, opts MessageListOptions) ([]*Message, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return store.ListMessages(ctx, r.ORM(), chatID, opts)
}

// ListRecentTranscriptSiblingMessages returns, ascending, the newest
// `limit` messages with seq >= fromSeq on threads other than mainThreadID
// that are not spawn threads, bounded in SQL. The chat snapshot reads its
// sibling rows through this: node and fork threads render inline in the
// transcript, while a spawn renders as a tool-call card that fetches its
// own thread (ListMessages with thread_id) when expanded.
func (r *Repo) ListRecentTranscriptSiblingMessages(ctx context.Context, chatID, mainThreadID string, fromSeq int64, limit int) ([]*Message, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	if mainThreadID == "" {
		return nil, fmt.Errorf("main thread ID cannot be empty")
	}

	return store.ListRecentTranscriptSiblingMessages(ctx, r.ORM(), chatID, mainThreadID, fromSeq, limit)
}

// CountMessagesInChat returns the true message count for a chat.
func (r *Repo) CountMessagesInChat(ctx context.Context, chatID string) (int, error) {
	if chatID == "" {
		return 0, fmt.Errorf("chat ID cannot be empty")
	}

	return r.countMessages(ctx, orm.WhereEq(store.MessageFieldChatID, chatID))
}

// CountMessagesByContextWindow returns the row count of a single context
// window, for CW-chain-aware totals that don't require fetching rows.
func (r *Repo) CountMessagesByContextWindow(ctx context.Context, contextWindowID string) (int, error) {
	if contextWindowID == "" {
		return 0, fmt.Errorf("context window ID cannot be empty")
	}

	return r.countMessages(ctx, orm.WhereEq(store.MessageFieldContextWindowID, contextWindowID))
}

// CountMessagesByContextWindowUpToSeq counts messages in a context window
// with seq <= maxSeq -- the count-only mirror of the fork filter used
// when resolving inherited messages.
func (r *Repo) CountMessagesByContextWindowUpToSeq(ctx context.Context, contextWindowID string, maxSeq int64) (int, error) {
	if contextWindowID == "" {
		return 0, fmt.Errorf("context window ID cannot be empty")
	}

	return r.countMessages(ctx, orm.WhereEq(store.MessageFieldContextWindowID, contextWindowID), orm.WhereLte(store.MessageFieldSeq, maxSeq))
}

// ListRecentMessagesInContextWindowBeforeSeq returns the newest `limit`
// messages in a single context window strictly before beforeSeq (0 means
// unbounded), ascending, bounded in SQL.
func (r *Repo) ListRecentMessagesInContextWindowBeforeSeq(ctx context.Context, contextWindowID string, beforeSeq int64, limit int) ([]*Message, error) {
	if contextWindowID == "" {
		return nil, fmt.Errorf("context window ID cannot be empty")
	}

	return store.ListRecentMessagesInContextWindowBeforeSeq(ctx, r.ORM(), contextWindowID, beforeSeq, limit)
}

// ListTranscriptSiblingMessagesInRange returns, ascending, every message of
// the chat on a thread other than mainThreadID that is not a spawn thread,
// with seq >= fromSeq and, when toSeq is set, seq < toSeq; with limit > 0, the
// newest limit of them. It is the sibling half of a ListMessages page, from the
// same filter the chat snapshot uses.
func (r *Repo) ListTranscriptSiblingMessagesInRange(ctx context.Context, chatID, mainThreadID string, fromSeq int64, toSeq *int64, limit int) ([]*Message, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	if mainThreadID == "" {
		return nil, fmt.Errorf("main thread ID cannot be empty")
	}

	return store.ListTranscriptSiblingMessagesInRange(ctx, r.ORM(), chatID, mainThreadID, fromSeq, toSeq, limit)
}

// CountMessagesInChatOutsideThread counts the chat's messages that sit on
// threads other than threadID.
func (r *Repo) CountMessagesInChatOutsideThread(ctx context.Context, chatID, threadID string) (int, error) {
	if chatID == "" {
		return 0, fmt.Errorf("chat ID cannot be empty")
	}
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	return store.CountMessagesInChatOutsideThread(ctx, r.ORM(), chatID, threadID)
}

func (r *Repo) GetLatestMessageInThread(ctx context.Context, threadID string) (*Message, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	return store.LatestMessageInThread(ctx, r.ORM(), threadID)
}

// ThreadAwaitsReply reports whether a thread's latest turn — its newest
// message that is not a system note — is a user message no reply has
// followed.
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

func (r *Repo) getMaxContextSequenceInThread(ctx context.Context, threadID string) (int, error) {
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
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("content block"), id)
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
		return nil, fmt.Errorf("%w: %s", svcerr.NotFound("tool call"), toolCallID)
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

func (r *Repo) AppendToContentBlock(ctx context.Context, blockID string, delta string) error {
	if blockID == "" {
		return fmt.Errorf("content block ID cannot be empty")
	}
	return store.AppendToContentBlock(ctx, r.ORM(), blockID, delta)
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

// UpdateMessage updates a message.
func (r *Repo) UpdateMessage(ctx context.Context, msg *Message) error {
	if msg == nil {
		return fmt.Errorf("message cannot be nil")
	}
	if msg.ID == "" {
		return fmt.Errorf("message ID cannot be empty")
	}

	return store.UpdateMessageUsage(ctx, r.ORM(), msg.ID, msg.TokenCount, msg.Cost)
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

// SetProjectForgeName records forge's name for a project (forge.yaml
// `name`) and marks it a forge project. Reports whether the row changed.
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
var ErrDaemonOwnedByAnotherUser = svcerr.PermissionDenied("daemon is owned by another user")

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
	if err != nil && !svcerr.IsNotFound(err) {
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
// Used by the gateway's /flow-health endpoint, which cross-checks the rows
// against the streams it actually holds — the rows alone cannot say whether a
// daemon is still expected to be there.
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
// project has no record. Prefer it (and GetProjectPresetsJSON) to
// GetProjectConfigRecord whenever that column is all a caller needs: the full
// row carries every indexed skill body and can run to tens of MB.
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

// GetWorktreeByIdempotencyKey returns a prior create's worktree for this
// key, or (nil, nil) when there is none.
func (r *Repo) GetWorktreeByIdempotencyKey(ctx context.Context, projectID, key string) (*Worktree, error) {
	// An empty key means the caller opted out of idempotency, not that it is
	// asking about the rows that happen to have no key.
	if projectID == "" || key == "" {
		return nil, nil
	}
	return store.GetWorktreeByIdempotencyKey(ctx, r.ORM(), projectID, key)
}

// GetLiveWorktreeByName returns the project's unarchived worktree called
// name, or (nil, nil). At most one exists.
func (r *Repo) GetLiveWorktreeByName(ctx context.Context, projectID, name string) (*Worktree, error) {
	if projectID == "" || name == "" {
		return nil, nil
	}
	return store.GetLiveWorktreeByName(ctx, r.ORM(), projectID, name)
}

// ListWorktreesByBranch returns every row of the project recorded against
// the branch, archived ones included.
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

// ListMainWorktreesForUser returns the main workspace of each of the user's
// projects, or of the one project when projectID is set and the user owns it.
func (r *Repo) ListMainWorktreesForUser(ctx context.Context, userID string, projectID *string) ([]*Worktree, error) {
	return store.ListMainWorktreesForUser(ctx, r.ORM(), userID, projectID)
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

func (r *Repo) DeleteRepo(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("repo ID cannot be empty")
	}
	return store.RemoveRepo(ctx, r.ORM(), id)
}

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

func (r *Repo) getBoolOrDefault(ctx context.Context, userID string, projectID *string, key string, defaultVal bool) (bool, error) {
	setting, err := r.GetSetting(ctx, userID, projectID, key)
	if err != nil {
		// If setting not found, return default
		if svcerr.IsNotFound(err) {
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

func (r *Repo) SetString(ctx context.Context, userID string, projectID *string, key, value string) error {
	return r.upsertSetting(ctx, userID, projectID, key, value, "string")
}

func (r *Repo) setBool(ctx context.Context, userID string, projectID *string, key string, value bool) error {
	return r.upsertSetting(ctx, userID, projectID, key, strconv.FormatBool(value), "bool")
}

func (r *Repo) upsertSetting(ctx context.Context, userID string, projectID *string, key, value, valueType string) error {
	setting, err := r.GetSetting(ctx, userID, projectID, key)
	if err != nil {
		// If not found, create new setting
		if svcerr.IsNotFound(err) {
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
	return r.apiKeys.GetAutomationToken(ctx, userID, daemonID)
}

func (r *Repo) SetAutomationToken(ctx context.Context, userID, daemonID, token string) error {
	if userID == "" || daemonID == "" {
		return fmt.Errorf("user_id and daemon_id cannot be empty")
	}
	return r.apiKeys.SetAutomationToken(ctx, userID, daemonID, token)
}

func (r *Repo) DeleteAutomationToken(ctx context.Context, userID, daemonID string) error {
	if userID == "" || daemonID == "" {
		return fmt.Errorf("user_id and daemon_id cannot be empty")
	}
	return r.apiKeys.DeleteAutomationToken(ctx, userID, daemonID)
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

func (r *Repo) UpdatePlan(ctx context.Context, plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("plan cannot be nil")
	}
	if plan.ID == "" {
		return fmt.Errorf("plan ID cannot be empty")
	}

	return store.ReplacePlan(ctx, r.ORM(), plan)
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
	return r.threadTokenCount(ctx, thread, maxSeq, nil)
}

// threadTokenCount is GetThreadTokenCount for a caller that has already
// resolved the thread's current context sequence (knownContextSeq), saving the
// repeat lookup. Inherited parents resolve their own.
func (r *Repo) threadTokenCount(ctx context.Context, thread string, maxSeq *int64, knownContextSeq *int) (int64, error) {
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
	var contextSeq int
	if knownContextSeq != nil {
		contextSeq = *knownContextSeq
	} else if contextSeq, err = r.getMaxContextSequenceInThread(ctx, thread); err != nil {
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

	contextSeq, err := r.getMaxContextSequenceInThread(ctx, thread)
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

	// The current context sequence is the same for all three reads below.
	contextSeq, err := r.getMaxContextSequenceInThread(ctx, thread)
	if err != nil {
		return nil, fmt.Errorf("failed to get max context sequence: %w", err)
	}

	// The common case: the thread's current window has a message carrying
	// token data. That one row is both the count (its token_count) and the
	// denominator (the threshold of the model that produced it), so read it
	// once. The general path below reads it twice, behind a thread lookup it
	// only needs for fork inheritance — four serial round trips on the chat
	// open path where this is two.
	if msg, err := store.LatestMessageWithTokens(ctx, r.ORM(), thread, int64(contextSeq), nil); err == nil &&
		msg != nil && msg.TokenCount != nil && *msg.TokenCount > 0 {
		threshold := models.UnknownModelCompactionFloor
		if msg.Model != nil {
			threshold = models.CompactionThresholdForModel(*msg.Model)
		}
		return &ContextUsage{
			ThreadTokenCount:    int64(*msg.TokenCount),
			CompactionThreshold: int64(threshold),
			CurrentContextSeq:   int64(contextSeq),
		}, nil
	}

	// No local token data: the count and the threshold are inherited from a
	// fork's parent, or are zero. Get token count using unified function
	// (handles fork inheritance)
	tokens, err := r.threadTokenCount(ctx, thread, nil, &contextSeq)
	if err != nil {
		return nil, fmt.Errorf("failed to get thread token count: %w", err)
	}

	return &ContextUsage{
		ThreadTokenCount:    tokens,
		CompactionThreshold: int64(r.resolveThreadCompactionThreshold(ctx, thread, nil, &contextSeq)),
		CurrentContextSeq:   int64(contextSeq),
	}, nil
}

// resolveThreadCompactionThreshold returns the compaction threshold for the
// model that produced the thread's current token count, mirroring the fork
// inheritance walk in GetThreadTokenCount so the indicator's denominator tracks
// the same model the trigger evaluates. Falls back to the global default when
// no model-bearing message is found. maxSeq bounds inheritance to a fork
// point, the same way maxOrdinal used to -- see GetThreadTokenCount.
func (r *Repo) resolveThreadCompactionThreshold(ctx context.Context, thread string, maxSeq *int64, knownContextSeq *int) int {
	if thread == "" {
		return models.UnknownModelCompactionFloor
	}

	var contextSeq int
	if knownContextSeq != nil {
		contextSeq = *knownContextSeq
	} else {
		var err error
		if contextSeq, err = r.getMaxContextSequenceInThread(ctx, thread); err != nil {
			contextSeq = 0
		}
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
	return r.resolveThreadCompactionThreshold(ctx, parentThreadID, &forkMsg.Seq, nil)
}

// ==================== Workflows ====================

func (r *Repo) CreateWorkflow(ctx context.Context, workflow *Workflow) error {
	if workflow == nil {
		return fmt.Errorf("workflow cannot be nil")
	}

	return r.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		if err := store.InsertWorkflowIfAbsent(txCtx, r.ORM(), workflow); err != nil {
			return err
		}
		if workflow.ChatID != "" {
			return r.emitChatActivityIfChanged(txCtx, workflow.ChatID)
		}
		return nil
	})
}

func (r *Repo) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	return store.LoadWorkflow(ctx, r.ORM(), id)
}

func (r *Repo) GetWorkflowByThread(ctx context.Context, chatID, thread string) (*Workflow, error) {
	return store.FindWorkflowByThread(ctx, r.ORM(), chatID, thread)
}

func (r *Repo) ListWorkflowsByChat(ctx context.Context, chatID string) ([]*Workflow, error) {
	return store.ListChatWorkflows(ctx, r.ORM(), chatID)
}

func (r *Repo) ListChildWorkflows(ctx context.Context, parentID string) ([]*Workflow, error) {
	return store.ListChildWorkflows(ctx, r.ORM(), parentID)
}

func (r *Repo) ListRootWorkflows(ctx context.Context, chatID string) ([]*Workflow, error) {
	return store.ListRootWorkflows(ctx, r.ORM(), chatID)
}

func (r *Repo) GetRootWorkflowStatusForChats(ctx context.Context, chatIDs []string) (map[string]WorkflowStatus, error) {
	if len(chatIDs) == 0 {
		return make(map[string]WorkflowStatus), nil
	}
	return store.RootWorkflowStatusForChats(ctx, r.ORM(), chatIDs)
}

// workflowStatusWrite is the transaction a workflow status write runs in when
// its caller has none. READ COMMITTED is enough: each write is ONE UPDATE of
// one row by id — a CAS keeps its expected status in the UPDATE's own WHERE,
// which Postgres re-checks against the row it waited for — and the read that
// follows only finds the chat to announce, after the commit. SERIALIZABLE
// added nothing to that and made every status write a party to predicate-lock
// conflicts with whatever else was inserting workflows, so under load a run's
// status writes (its launch's mark-active, its terminal status) failed with
// SQLSTATE 40001. Called inside a caller's transaction, they join it.
var workflowStatusWrite = orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts)

func (r *Repo) CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus WorkflowStatus) (bool, error) {
	var swapped bool
	err := r.RunTx(ctx, workflowStatusWrite, func(txCtx context.Context) error {
		ok, err := store.CompareAndSwapWorkflowStatus(txCtx, r.ORM(), id, newStatus, expectedStatus)
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

// MarkWorkflowStartFailed records that a PENDING workflow's start attempt
// failed (Workflow.StartFailedAt). No chat-activity emit: the row is still
// PENDING, so whether the chat is busy has not changed.
func (r *Repo) MarkWorkflowStartFailed(ctx context.Context, id string) (bool, error) {
	return store.MarkWorkflowStartFailed(ctx, r.ORM(), id)
}

// ClaimWorkflowStart clears a PENDING workflow's failed-start stamp before a
// retry of its start, reporting whether it is still PENDING (see
// store.ClaimWorkflowStart).
func (r *Repo) ClaimWorkflowStart(ctx context.Context, id string) (bool, error) {
	return store.ClaimWorkflowStart(ctx, r.ORM(), id)
}

// AbandonFailedWorkflowStart moves a PENDING workflow whose start failed to
// STOPPED/FAILED, reporting whether it did (see
// store.AbandonFailedWorkflowStart). Inside a caller's transaction it joins
// it, and the chat's activity is announced after that commits.
func (r *Repo) AbandonFailedWorkflowStart(ctx context.Context, id string) (bool, error) {
	var abandoned bool
	err := r.RunTx(ctx, workflowStatusWrite, func(txCtx context.Context) error {
		ok, err := store.AbandonFailedWorkflowStart(txCtx, r.ORM(), id)
		abandoned = ok
		if err != nil || !ok {
			return err
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
	return abandoned, err
}

// ListStrandedWorkflowStarts is the PENDING roots whose launch committed
// before cutoff and whose start was never recorded as failed (see
// store.ListStrandedWorkflowStarts).
func (r *Repo) ListStrandedWorkflowStarts(ctx context.Context, cutoff time.Time, limit int) ([]*Workflow, error) {
	return store.ListStrandedWorkflowStarts(ctx, r.ORM(), cutoff, limit)
}

func (r *Repo) UpdateWorkflowStatus(ctx context.Context, id string, status WorkflowStatus) error {
	return r.RunTx(ctx, workflowStatusWrite, func(txCtx context.Context) error {
		if err := store.UpdateWorkflowStatus(txCtx, r.ORM(), id, status); err != nil {
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
	return store.SetWorkflowOutcome(ctx, r.ORM(), id, outcome)
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
	return store.RenamePendingWorkflow(ctx, r.ORM(), id, workflowName)
}

// CascadeTerminalStatusToDescendants moves every running/paused descendant
// workflow record owned by a parent to the terminal status the parent itself
// reached. Called when a workflow ends, to drain the children (spawn
// children, thread records, etc.) that are still running or paused. The
// caller passes what actually happened: a descendant of a cancelled run is
// cancelled, not completed, and a supervision surface that counts completed
// units must not be handed a terminated subtree to count.
func (r *Repo) CascadeTerminalStatusToDescendants(ctx context.Context, parentWorkflowID string, reason WorkflowStopReason) error {
	return store.CascadeTerminalStatusToDescendants(ctx, r.ORM(), parentWorkflowID, reason)
}

// ReapOrphanedWorkflowDescendants ends every running/paused workflow whose
// parent is already terminal — at the terminal ancestor's own status — and
// returns the rows it moved. The backstop for a write path that reached
// a terminal status without cascading (every TerminateWorkflow path does: a
// hard kill skips the workflow's own completion handler). Nothing else
// revisits those rows — the reconciler skips workflows with a parent_id — so
// without this they are reported as running forever.
func (r *Repo) ReapOrphanedWorkflowDescendants(ctx context.Context) ([]ReapedWorkflow, error) {
	return store.ReapOrphanedWorkflowDescendants(ctx, r.ORM())
}

// ReviveSubtreeLiveAt is the inverse of the two terminal cascades for a
// subtree that is coming BACK: after a reset-and-replay, every descendant
// workflow row that was live at the reset point returns to active, along with
// the threads those rows own and the root's own thread. Returns how many of
// each it moved.
//
// A resume must revive what it resumes. Sub-agents run INLINE in the root's
// single Temporal execution but own their own rows, so killing the root
// stamped them failed — and replay rebuilds those same sub-agents WITHOUT
// re-running the "started" activity that is the only other write which moves
// a row back to running (it is in the replayed history). Without this call
// the chat resumes with live agents whose rows read failed, and the
// reconciler's stranded-spawn sweep then writes false "the parent had already
// exited" reports against them. Measured: six of each,
// dev-docs/incidents/2026-09-29-reconciler-false-wedge.md.
//
// `at` is the reset point's event time; it is what separates work the new run
// will redo from work it will only replay. Paused rows are out of scope
// (ResumeWorkflowsByChat owns those). See internal/store/workflow_repo_ext.go
// (reviveSubtreeLiveAtSQL) for the predicate and for why both halves are a
// single atomic statement.
func (r *Repo) ReviveSubtreeLiveAt(ctx context.Context, rootWorkflowID string, at time.Time) (workflowsRevived, threadsRevived int64, err error) {
	if rootWorkflowID == "" {
		return 0, 0, fmt.Errorf("root workflow ID cannot be empty")
	}
	return store.ReviveSubtreeLiveAt(ctx, r.ORM(), rootWorkflowID, at)
}

func (r *Repo) PauseRunningWorkflowsByChat(ctx context.Context, chatID string) error {
	return r.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		if err := store.PauseRunningWorkflowsByChat(txCtx, r.ORM(), chatID); err != nil {
			return err
		}
		return r.emitChatActivityIfChanged(txCtx, chatID)
	})
}

func (r *Repo) ResumeWorkflowsByChat(ctx context.Context, chatID string) error {
	return r.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		if err := store.ResumeWorkflowsByChat(txCtx, r.ORM(), chatID); err != nil {
			return err
		}
		return r.emitChatActivityIfChanged(txCtx, chatID)
	})
}

// ==================== Triggers ====================
//
// Every method here delegates to internal/store over r.ORM(), which joins the
// transaction ctx carries — so they pick up an ambient RunTx transaction
// automatically. That is what lets the launcher write the trigger event row in
// the SAME transaction as the chat it launches: either both commit or neither
// does, and there is no window where a chat exists with nothing recording why.

// mapTriggerNameConflict turns a unique violation on the trigger's
// (user, project, name) key into core.ErrTriggerNameTaken: AlreadyExists to the
// caller instead of an opaque Internal "database error". Like
// mapWorkflowSlugConflict, the PgError is not wrapped (%w), so the retry ladder
// does not re-run a permanent conflict.
func mapTriggerNameConflict(err error, name string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == store.TriggerConstraintUserIDProjectIDNameKey {
		return fmt.Errorf("%w: %q", core.ErrTriggerNameTaken, name)
	}
	return err
}

func (r *Repo) CreateTrigger(ctx context.Context, t *core.Trigger) error {
	if t == nil {
		return fmt.Errorf("trigger cannot be nil")
	}
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if err := store.InsertTrigger(ctx, r.ORM(), t); err != nil {
			return mapTriggerNameConflict(err, t.Name)
		}
		after, err := store.GetTrigger(ctx, r.ORM(), t.ID)
		if err != nil {
			return err
		}
		return store.RecordTriggerChange(ctx, r.ORM(), "create", nil, after)
	})
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

// LockTrigger row-locks the trigger until the surrounding transaction ends.
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
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if err := store.LockTrigger(ctx, r.ORM(), t.ID); err != nil {
			return err
		}
		before, err := store.GetTrigger(ctx, r.ORM(), t.ID)
		if err != nil {
			return err
		}
		if revision, ok := automationpolicy.Revision(ctx); ok && !before.UpdatedAt.Equal(revision) {
			return svcerr.FailedPrecondition("Activation changed after approval; request a new approval")
		}
		if err := store.ReplaceTrigger(ctx, r.ORM(), t); err != nil {
			return mapTriggerNameConflict(err, t.Name)
		}
		after, err := store.GetTrigger(ctx, r.ORM(), t.ID)
		if err != nil {
			return err
		}
		return store.RecordTriggerChange(ctx, r.ORM(), "update", before, after)
	})
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
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if err := store.LockTrigger(ctx, r.ORM(), id); err != nil {
			return err
		}
		before, err := store.GetTrigger(ctx, r.ORM(), id)
		if err != nil {
			return err
		}
		if revision, ok := automationpolicy.Revision(ctx); ok && !before.UpdatedAt.Equal(revision) {
			return svcerr.FailedPrecondition("Activation changed after approval; request a new approval")
		}
		if err := store.SetTriggerEnabled(ctx, r.ORM(), id, enabled); err != nil {
			return err
		}
		after, err := store.GetTrigger(ctx, r.ORM(), id)
		if err != nil {
			return err
		}
		op := "pause"
		if enabled {
			op = "enable"
		}
		return store.RecordTriggerChange(ctx, r.ORM(), op, before, after)
	})
}

func (r *Repo) LockActivationDependencies(ctx context.Context, userID, projectID string) error {
	return store.LockActivationDependencies(ctx, r.ORM(), userID, projectID)
}

func (r *Repo) ListTriggerChanges(ctx context.Context, userID, triggerID string) ([]core.TriggerChange, error) {
	return store.TriggerChanges(ctx, r.ORM(), userID, triggerID)
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

// CreateTriggerEvent records one firing. The insert is ON CONFLICT
// (kind, dedupe_key) DO NOTHING, so created=false means a firing for that
// key already existed and nothing was written. That constraint is what
// makes "a chat starts exactly once" and "a retried scheduled fire cannot
// launch twice" database guarantees rather than client conventions.
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

// GetTriggerEventByChat returns the event of the given kind that launched
// chatID (core.ErrTriggerEventNotFound when none did).
func (r *Repo) GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return store.GetTriggerEventByChat(ctx, r.ORM(), kind, chatID)
}

// CountLiveLaunchedRuns counts the user's chats launched by this kind of
// event whose root run is still live (pending, active or paused).
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

// SetLaunchEventRunFailureDetail records why the launch run failed.
func (r *Repo) SetLaunchEventRunFailureDetail(ctx context.Context, chatID, detail string) error {
	return store.SetLaunchEventRunFailureDetail(ctx, r.ORM(), chatID, detail)
}

// SetLaunchEventRunStatus records how the run a trigger fired ended, on
// the chat's launch event (core.TriggerEvent.RunStatus).
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

// ListTriggerEvents returns one page of the trigger's firings newest first,
// each with the run it launched, and whether more follow.
func (r *Repo) ListTriggerEvents(ctx context.Context, f core.TriggerEventFilters) ([]*core.TriggerEventWithRun, bool, error) {
	if f.TriggerID == "" {
		return nil, false, fmt.Errorf("trigger ID cannot be empty")
	}
	if f.UserID == "" {
		return nil, false, fmt.Errorf("user ID cannot be empty")
	}
	return store.ListTriggerEventsPage(ctx, r.ORM(), f)
}

// ListInboxPending, ListDismissedInboxItemIDs, DismissInboxItems and
// RestoreInboxItems back the Inbox. Each is scoped by an explicit user id.
func (r *Repo) ListInboxPending(ctx context.Context, userID string) ([]*core.InboxPending, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.ListInboxPending(ctx, r.ORM(), userID)
}

func (r *Repo) ListDismissedInboxItemIDs(ctx context.Context, userID string, itemIDs []string) (map[string]bool, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.ListDismissedInboxItemIDs(ctx, r.ORM(), userID, itemIDs)
}

func (r *Repo) DismissInboxItems(ctx context.Context, userID string, itemIDs []string, at time.Time) error {
	if userID == "" {
		return fmt.Errorf("user ID cannot be empty")
	}
	return store.DismissInboxItems(ctx, r.ORM(), userID, itemIDs, at)
}

func (r *Repo) RestoreInboxItems(ctx context.Context, userID string, itemIDs []string) error {
	if userID == "" {
		return fmt.Errorf("user ID cannot be empty")
	}
	return store.RestoreInboxItems(ctx, r.ORM(), userID, itemIDs)
}

// CreateRunEvent inserts the event, doing nothing when its DedupeKey
// already exists. created=false means the transition was already recorded.
func (r *Repo) CreateRunEvent(ctx context.Context, ev *core.RunEvent) (bool, error) {
	if ev == nil {
		return false, fmt.Errorf("run event cannot be nil")
	}
	if ev.DedupeKey == "" {
		// An empty key would collide every transition with every other one,
		// and the second real event would silently read as a retry.
		return false, fmt.Errorf("run event dedupe key cannot be empty")
	}
	return store.InsertRunEvent(ctx, r.ORM(), ev)
}

// GetRunEvent returns ErrRunEventNotFound on a miss.
func (r *Repo) GetRunEvent(ctx context.Context, id string) (*core.RunEvent, error) {
	if id == "" {
		return nil, fmt.Errorf("run event ID cannot be empty")
	}
	return store.LoadRunEvent(ctx, r.ORM(), id)
}

// GetRunEventByDedupe returns ErrRunEventNotFound on a miss.
func (r *Repo) GetRunEventByDedupe(ctx context.Context, dedupeKey string) (*core.RunEvent, error) {
	if dedupeKey == "" {
		return nil, fmt.Errorf("run event dedupe key cannot be empty")
	}
	return store.LoadRunEventByDedupe(ctx, r.ORM(), dedupeKey)
}

// HasEnabledTriggerOfKind reports whether the user owns an enabled trigger
// of the kind.
func (r *Repo) HasEnabledTriggerOfKind(ctx context.Context, userID string, kind core.TriggerKind) (bool, error) {
	if userID == "" {
		return false, fmt.Errorf("user ID cannot be empty")
	}
	return store.HasEnabledTriggerOfKind(ctx, r.ORM(), userID, kind)
}

// LockChatForRunEvent row-locks the chat until the transaction ends.
func (r *Repo) LockChatForRunEvent(ctx context.Context, chatID string) error {
	if chatID == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	return store.LockChatForRunEvent(ctx, r.ORM(), chatID)
}

// CountOtherPendingBlockers counts the chat's pending approvals and
// questions other than excludeID.
func (r *Repo) CountOtherPendingBlockers(ctx context.Context, chatID, excludeID string) (int, error) {
	if chatID == "" {
		return 0, fmt.Errorf("chat ID cannot be empty")
	}
	return store.CountOtherPendingBlockers(ctx, r.ORM(), chatID, excludeID)
}

// ClaimRunEvents leases up to max undispatched events until leaseUntil.
func (r *Repo) ClaimRunEvents(ctx context.Context, now, leaseUntil time.Time, max int) ([]*core.RunEvent, error) {
	if max <= 0 {
		return nil, nil
	}
	return store.ClaimRunEvents(ctx, r.ORM(), now, leaseUntil, max)
}

// MarkRunEventDispatched records that the event's dispatch has started.
func (r *Repo) MarkRunEventDispatched(ctx context.Context, id string, at time.Time) error {
	if id == "" {
		return fmt.Errorf("run event ID cannot be empty")
	}
	return store.MarkRunEventDispatched(ctx, r.ORM(), id, at)
}

// DeleteDispatchedRunEventsBefore prunes dispatched events older than
// cutoff, returning how many were removed.
func (r *Repo) DeleteDispatchedRunEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return store.DeleteDispatchedRunEventsBefore(ctx, r.ORM(), cutoff)
}

// RecentTriggerFirings returns each trigger's newest perTrigger firings in
// one query. Used to compute health and last_event for a list of triggers.
func (r *Repo) RecentTriggerFirings(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.RecentTriggerFirings(ctx, r.ORM(), userID, triggerIDs, perTrigger)
}

// RecentTriggerOutcomes is RecentTriggerFirings without the events' bodies
// (payload, sender, origin), for health that is judged but never shown.
func (r *Repo) RecentTriggerOutcomes(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.RecentTriggerOutcomes(ctx, r.ORM(), userID, triggerIDs, perTrigger)
}

// FiringsSinceLastSuccess returns each trigger's firings since its newest
// success, newest first, capped at perTrigger: the current failure episode.
// The events carry no body (payload, sender, origin): an episode is counted.
func (r *Repo) FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.FiringsSinceLastSuccess(ctx, r.ORM(), userID, triggerIDs, perTrigger)
}

// GetLatestTriggerEvent returns the trigger's latest firing, optionally
// filtered by outcome, and (nil, nil) when it has never fired.
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

func (r *Repo) ClaimPendingTriggerEvent(ctx context.Context, id string, payload map[string]any, def *core.TriggerEventDefinition) (bool, error) {
	return store.ClaimPendingTriggerEvent(ctx, r.ORM(), id, payload, def)
}

func (r *Repo) SettlePendingTriggerEvent(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string, def *core.TriggerEventDefinition) (bool, error) {
	return store.SettlePendingTriggerEvent(ctx, r.ORM(), id, outcome, detail, def)
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

	return store.InsertStepExecution(ctx, r.ORM(), exec)
}

func (r *Repo) GetStepExecution(ctx context.Context, id string) (*StepExecution, error) {
	if id == "" {
		return nil, fmt.Errorf("step execution ID cannot be empty")
	}

	return store.LoadStepExecution(ctx, r.ORM(), id)
}

func (r *Repo) GetBasicStepExecutionsForChat(ctx context.Context, chatID string) ([]*ChatStepExecution, error) {
	return store.ListBasicChatStepExecutions(ctx, r.ORM(), chatID)
}

// GetStepExecutionsForChat is the FULL view: output only for user-facing
// activities. model.InternalActivities crosses into SQL here and only here.
func (r *Repo) GetStepExecutionsForChat(ctx context.Context, chatID string) ([]*ChatStepExecution, error) {
	return store.ListChatStepExecutions(ctx, r.ORM(), chatID, model.InternalActivities)
}

// ListRunNodeExecutions returns bounded diagnostics for one workflow subtree.
func (r *Repo) ListRunNodeExecutions(ctx context.Context, workflowID string, limit int) ([]*StepExecution, error) {
	return store.ListRunNodeExecutions(ctx, r.ORM(), workflowID, limit)
}

// MarkDeclaredFailureNode records that a run's top-level node declared the
// run's outcome: failure (store.MarkDeclaredFailureNode).
func (r *Repo) MarkDeclaredFailureNode(ctx context.Context, workflowID, nodeID string) (bool, error) {
	return store.MarkDeclaredFailureNode(ctx, r.ORM(), workflowID, nodeID)
}

// ListStepExecutionRecordsForChat returns full step rows (inputs, output,
// error), newest first, optionally scoped to one node and what ran inside it.
func (r *Repo) ListStepExecutionRecordsForChat(ctx context.Context, chatID, nodePath string, limit int) ([]*StepExecution, error) {
	return store.ListStepExecutionRecordsForChat(ctx, r.ORM(), chatID, nodePath, limit)
}

func (r *Repo) GetStepExecutionsByWorkflow(ctx context.Context, workflowID string) ([]*StepExecution, error) {
	return store.ListWorkflowStepExecutions(ctx, r.ORM(), workflowID)
}

func (r *Repo) ListWorkflowsByStatus(ctx context.Context, status WorkflowStatus) ([]*Workflow, error) {
	return store.ListWorkflowsByStatus(ctx, r.ORM(), status)
}

// ==================== Workflow Checkpoints ====================
// Position truth for resume-at-position: last top-level node entered (and
// loop iteration for loop nodes) per workflow ID.

func (r *Repo) UpsertWorkflowCheckpoint(ctx context.Context, checkpoint *WorkflowCheckpoint) error {
	if checkpoint == nil {
		return fmt.Errorf("checkpoint cannot be nil")
	}
	return store.SaveWorkflowCheckpoint(ctx, r.ORM(), checkpoint)
}

// GetWorkflowCheckpoint returns the recorded position for a workflow ID, or
// (nil, nil) when no checkpoint exists.
func (r *Repo) GetWorkflowCheckpoint(ctx context.Context, workflowID string) (*WorkflowCheckpoint, error) {
	return store.LoadWorkflowCheckpoint(ctx, r.ORM(), workflowID)
}

func (r *Repo) DeleteWorkflowCheckpoint(ctx context.Context, workflowID string) error {
	return store.ClearWorkflowCheckpoint(ctx, r.ORM(), workflowID)
}

// ==================== Threads ====================
// First-class entity for thread hierarchy and fork relationships

// CreateThread inserts the thread. Thread.Origin (main/spawn/fork/node) is a
// stored column, not re-derived
// at read time from a workflow row's spawned_by_node_id sentinel -- see
// core.ThreadOrigin and 20260729000000_add_origin_to_threads.sql.
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
	return store.InsertThread(ctx, r.ORM(), thread)
}

func (r *Repo) GetThread(ctx context.Context, id string) (*Thread, error) {
	if id == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return store.GetThread(ctx, r.ORM(), id)
}

func (r *Repo) ListThreadsByIDs(ctx context.Context, ids []string) ([]*Thread, error) {
	return store.ListThreadsByIDs(ctx, r.ORM(), ids)
}

func (r *Repo) GetRootThread(ctx context.Context, chatID string) (*Thread, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return store.GetRootThread(ctx, r.ORM(), chatID)
}

func (r *Repo) GetThreadWithParent(ctx context.Context, id string) (*Thread, *string, error) {
	if id == "" {
		return nil, nil, fmt.Errorf("thread ID cannot be empty")
	}
	return store.GetThreadWithParent(ctx, r.ORM(), id)
}

func (r *Repo) ListThreadsByConversation(ctx context.Context, chatID string) ([]*Thread, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return store.ListThreadsByConversation(ctx, r.ORM(), chatID)
}

func (r *Repo) ListChildThreads(ctx context.Context, parentThreadID string) ([]*Thread, error) {
	if parentThreadID == "" {
		return nil, fmt.Errorf("parent thread ID cannot be empty")
	}
	return store.ListChildThreads(ctx, r.ORM(), parentThreadID)
}

func (r *Repo) UpdateThreadWorkflow(ctx context.Context, threadID, workflowID string) (*Thread, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return store.UpdateThreadWorkflow(ctx, r.ORM(), threadID, workflowID)
}

func (r *Repo) UpdateThreadStatus(ctx context.Context, threadID string, status ThreadStatus, completedAt *time.Time) (*Thread, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	updated, err := store.UpdateThreadStatus(ctx, r.ORM(), threadID, status, completedAt)
	if err != nil || !threadEndClosesQuestions(status) {
		return updated, err
	}
	// A thread that finished has no pending question (question_close.go).
	if err := r.closePendingQuestionsOfThreads(ctx, threadID); err != nil {
		return updated, err
	}
	return updated, nil
}

// ReviveThread moves a terminal thread back to running because a new run has
// started on it, and clears the completed_at of the turn that ended. The
// inverse of CascadeTerminalStatusToThreadSubtree, and the missing half of
// the thread lifecycle: a chat's main thread is reused for every turn, so the
// "started" write that revives the WORKFLOW row must revive the THREAD row
// with it. Without this, every chat from its second turn onward ran with a
// live workflow behind a thread still reading completed — which is what made
// SendAgentMessage refuse a working agent with "This agent has already
// finished". Returns the number of rows moved; 0 means the thread was not
// terminal (a no-op, not an error).
func (r *Repo) ReviveThread(ctx context.Context, threadID string) (int64, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}
	return store.ReviveThread(ctx, r.ORM(), threadID)
}

// CascadeTerminalStatusToThreadSubtree closes every thread owned by
// workflowID or any of its descendants to the terminal status the workflow
// subtree just reached — the thread-lifecycle counterpart of
// CascadeTerminalStatusToDescendants. threads.status is otherwise written
// ONLY by ThreadStatusActivity on the live path, so a workflow ending
// abnormally (terminate, reap, any write path that forgets this call) leaves
// its thread at running (2) forever. Measured on the live DB: 288 threads
// stranded this way (see dev-docs/incidents/2026-08-12-spawn-history-cap.md),
// which also makes their own orphaned mailboxes invisible to
// ListThreadsWithOrphanedAgentMessages (it only matches threads already in a
// terminal status). A subtree that completed or was cancelled has its pending
// questions closed with its threads (question_close.go), as
// UpdateThreadStatus and ReapOrphanedThreads do for the same statuses.
//
// It takes the same stop reason the callers pass to
// CascadeTerminalStatusToDescendants — the two calls are meant to travel
// together at every site.
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
	if err := store.CascadeTerminalStatusToThreadSubtree(ctx, r.ORM(), workflowID, threadStatus); err != nil {
		return err
	}
	// A subtree that finished closes its questions with its threads
	// (question_close.go).
	if !threadEndClosesQuestions(threadStatus) {
		return nil
	}
	if _, err := r.closePendingQuestionsInWorkflowSubtree(ctx, workflowID); err != nil {
		return err
	}
	return nil
}

// ReapOrphanedThreads ends every running/paused thread whose workflow is
// already terminal, at the workflow's own status, and returns the rows it
// moved. The backstop for a write path that reached a terminal workflow
// status without cascading to the thread — it mirrors
// ReapOrphanedWorkflowDescendants for the same reason.
func (r *Repo) ReapOrphanedThreads(ctx context.Context) ([]ReapedThread, error) {
	reaped, err := store.ReapOrphanedThreads(ctx, r.ORM())
	if err != nil || len(reaped) == 0 {
		return reaped, err
	}
	// A reaped thread that finished closes its questions with it
	// (question_close.go).
	var threadIDs []string
	for _, t := range reaped {
		if threadEndClosesQuestions(t.Status) {
			threadIDs = append(threadIDs, t.ThreadID)
		}
	}
	if err := r.closePendingQuestionsOfThreads(ctx, threadIDs...); err != nil {
		return reaped, err
	}
	return reaped, nil
}

func (r *Repo) ListThreadsByOrigin(ctx context.Context, conversationID string, origin ThreadOrigin) ([]*Thread, error) {
	if conversationID == "" {
		return nil, fmt.Errorf("conversation ID cannot be empty")
	}
	return store.ListThreadsByOrigin(ctx, r.ORM(), conversationID, origin)
}

func (r *Repo) CountThreadsInConversation(ctx context.Context, chatID string) (int64, error) {
	if chatID == "" {
		return 0, fmt.Errorf("chat ID cannot be empty")
	}
	return store.CountThreadsInConversation(ctx, r.ORM(), chatID)
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
	// One transaction: the insert raises the thread's context_window counter.
	var created *ContextWindow
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		created, err = store.InsertContextWindow(ctx, r.ORM(), cw)
		return err
	})
	return created, err
}

// AllocateContextWindowSequence takes the sequence of threadID's next context
// window from its counter (store.AllocateContextWindowSequence), never from
// the highest window already written.
func (r *Repo) AllocateContextWindowSequence(ctx context.Context, threadID string) (int, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}
	var seq int64
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		seq, err = store.AllocateContextWindowSequence(ctx, r.ORM(), threadID)
		return err
	})
	return int(seq), err
}

func (r *Repo) GetContextWindow(ctx context.Context, id string) (*ContextWindow, error) {
	if id == "" {
		return nil, fmt.Errorf("context window ID cannot be empty")
	}
	return store.GetContextWindow(ctx, r.ORM(), id)
}

func (r *Repo) GetLatestContextWindow(ctx context.Context, threadID string) (*ContextWindow, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return store.GetLatestContextWindow(ctx, r.ORM(), threadID)
}

func (r *Repo) GetContextWindowBySequence(ctx context.Context, threadID string, sequence int) (*ContextWindow, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return store.GetContextWindowBySequence(ctx, r.ORM(), threadID, sequence)
}

func (r *Repo) ListForkedThreadIDs(ctx context.Context, threadIDs []string) ([]string, error) {
	if len(threadIDs) == 0 {
		return nil, nil
	}
	return store.ListForkedThreadIDs(ctx, r.ORM(), threadIDs)
}

func (r *Repo) GetContextWindowWithThread(ctx context.Context, id string) (*ContextWindow, string, *string, *string, error) {
	if id == "" {
		return nil, "", nil, nil, fmt.Errorf("context window ID cannot be empty")
	}
	return store.GetContextWindowWithThread(ctx, r.ORM(), id)
}

func (r *Repo) ListContextWindowsByThread(ctx context.Context, threadID string) ([]*ContextWindow, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return store.ListContextWindowsByThread(ctx, r.ORM(), threadID)
}

func (r *Repo) GetMaxSequenceForThread(ctx context.Context, threadID string) (int, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}
	return store.GetMaxSequenceForThread(ctx, r.ORM(), threadID)
}

func (r *Repo) SetCompactionSummaryMessage(ctx context.Context, cwID, messageID string) (*ContextWindow, error) {
	if cwID == "" {
		return nil, fmt.Errorf("context window ID cannot be empty")
	}
	return store.SetCompactionSummaryMessage(ctx, r.ORM(), cwID, messageID)
}

// ==================== Workflow Drafts ==
// User-owned workflows available across all projects

// ErrWorkflowSlugTaken reports that the owner already has a workflow with the
// slug. It is deliberately not retryable: the conflict is permanent, not a race.
var ErrWorkflowSlugTaken = svcerr.AlreadyExists("workflow slug already taken")

// runWorkflowDraftTx: each draft write is one single-row write plus the
// user_updates insert, which allocates from the per-user counter row and so
// needs READ COMMITTED (see CreateUserUpdate). Under SERIALIZABLE it would
// abort against any concurrent update for the same user. The Repository's
// ladder still re-runs it on the failures it retries.
func (r *Repo) runWorkflowDraftTx(ctx context.Context, f func(txCtx context.Context) error) error {
	return r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), f)
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
		if err := store.InsertWorkflowDraft(txCtx, r.ORM(), draft); err != nil {
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
		row, err := store.UpsertWorkflowDraftBySlug(txCtx, r.ORM(), draft)
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
	row, err := store.LoadWorkflowDraft(ctx, r.ORM(), id)
	if err != nil {
		return err
	}
	return r.publishWorkflowDraftUpdated(ctx, row, false)
}

func (r *Repo) GetWorkflowDraft(ctx context.Context, id string) (*WorkflowDraft, error) {
	row, err := store.LoadWorkflowDraft(ctx, r.ORM(), id)
	if err != nil {
		if svcerr.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("workflow draft"), id)
		}
		return nil, err
	}
	return row, nil
}

func (r *Repo) GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error) {
	return store.FindWorkflowDraftBySlug(ctx, r.ORM(), userID, slug)
}

func (r *Repo) GetWorkflowDraftByName(ctx context.Context, userID, name string) (*WorkflowDraft, error) {
	return store.FindWorkflowDraftByName(ctx, r.ORM(), userID, name)
}

// GetUsableWorkflowBySlug returns the runnable (complete, visible) workflow
// with this slug, or (nil, nil) when there is none. A visible DRAFT with the
// slug is reported as *WorkflowDraftNotRunnableError rather than hidden behind
// "not found": the workflow exists, and the fix is to mark it complete.
func (r *Repo) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error) {
	usable, err := store.FindUsableWorkflowBySlug(ctx, r.ORM(), userID, slug)
	if err != nil || usable != nil {
		return usable, err
	}
	draft, err := store.FindWorkflowDraftBySlug(ctx, r.ORM(), userID, slug)
	if err != nil {
		return nil, err
	}
	if draft != nil && !draft.IsHidden && draft.Status != WorkflowDraftStatusComplete {
		return nil, &WorkflowDraftNotRunnableError{Slug: slug}
	}
	return nil, nil
}

func (r *Repo) ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*WorkflowDraft, error) {
	return store.ListUserWorkflowDrafts(ctx, r.ORM(), userID)
}

func (r *Repo) UpdateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error {
	if draft == nil {
		return fmt.Errorf("draft cannot be nil")
	}
	return r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		if err := store.ReplaceWorkflowDraft(txCtx, r.ORM(), draft); err != nil {
			return mapWorkflowSlugConflict(err, draft.Slug)
		}
		return r.publishWorkflowDraftUpdatedByID(txCtx, draft.ID)
	})
}

func (r *Repo) UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, status WorkflowDraftStatus) error {
	return r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		if err := store.SetWorkflowDraftDefinition(txCtx, r.ORM(), id, name, slug, definition, status); err != nil {
			return mapWorkflowSlugConflict(err, slug)
		}
		return r.publishWorkflowDraftUpdatedByID(txCtx, id)
	})
}

func (r *Repo) SetWorkflowDraftStatus(ctx context.Context, id string, status WorkflowDraftStatus) (*WorkflowDraft, error) {
	var saved *WorkflowDraft
	err := r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		row, err := store.SetWorkflowDraftStatus(txCtx, r.ORM(), id, status)
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
		row, err := store.SetWorkflowDraftHidden(txCtx, r.ORM(), id, isHidden)
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

func (r *Repo) DeleteWorkflowDraftBySlug(ctx context.Context, userID, slug string) error {
	return r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		deleted, err := store.RemoveWorkflowDraftBySlug(txCtx, r.ORM(), userID, slug)
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
	return store.WorkflowSlugExists(ctx, r.ORM(), userID, slug)
}

func (r *Repo) UpdateWorkflowForkedFrom(ctx context.Context, draftID string, forkedFrom string) (*WorkflowDraft, error) {
	var saved *WorkflowDraft
	err := r.runWorkflowDraftTx(ctx, func(txCtx context.Context) error {
		row, err := store.SetWorkflowDraftForkedFrom(txCtx, r.ORM(), draftID, forkedFrom)
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

// ==================== Workflow Scenario Repository ====================

func (r *Repo) CreateWorkflowScenario(ctx context.Context, scenario *WorkflowScenario) error {
	if scenario == nil {
		return fmt.Errorf("scenario cannot be nil")
	}
	return store.InsertWorkflowScenario(ctx, r.ORM(), scenario)
}

func (r *Repo) GetWorkflowScenario(ctx context.Context, id string) (*WorkflowScenario, error) {
	return store.FindWorkflowScenario(ctx, r.ORM(), id)
}

func (r *Repo) GetWorkflowScenarioByName(ctx context.Context, workflowDraftID string, name string) (*WorkflowScenario, error) {
	return store.FindWorkflowScenarioByName(ctx, r.ORM(), workflowDraftID, name)
}

func (r *Repo) ListWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) ([]*WorkflowScenario, error) {
	return store.ListDraftWorkflowScenarios(ctx, r.ORM(), workflowDraftID)
}

func (r *Repo) UpdateWorkflowScenario(ctx context.Context, scenario *WorkflowScenario) error {
	if scenario == nil {
		return fmt.Errorf("scenario cannot be nil")
	}
	return store.ReplaceWorkflowScenario(ctx, r.ORM(), scenario)
}

func (r *Repo) UpdateWorkflowScenarioResult(ctx context.Context, id string, status string, result string) error {
	return store.RecordWorkflowScenarioResult(ctx, r.ORM(), id, status, result)
}

func (r *Repo) DeleteWorkflowScenario(ctx context.Context, id string) error {
	return store.RemoveWorkflowScenario(ctx, r.ORM(), id)
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
	if err := store.InsertQuestion(ctx, r.ORM(), question); err != nil {
		return err
	}
	_ = r.emitChatActivityIfChanged(ctx, question.ChatID)
	return nil
}

func (r *Repo) GetQuestionByID(ctx context.Context, id string) (*Question, error) {
	if id == "" {
		return nil, fmt.Errorf("question ID cannot be empty")
	}
	return store.GetQuestion(ctx, r.ORM(), id)
}

func (r *Repo) GetQuestionsByWorkflowStepIteration(ctx context.Context, workflowID, stepID string, iteration int) ([]*Question, error) {
	return store.ListQuestionsByWorkflowStepIteration(ctx, r.ORM(), workflowID, stepID, iteration)
}

func (r *Repo) ResolveQuestion(ctx context.Context, id string, responseData *string) error {
	if id == "" {
		return fmt.Errorf("question ID cannot be empty")
	}
	if err := store.ResolveQuestion(ctx, r.ORM(), id, responseData, time.Now().UTC()); err != nil {
		return err
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

// EnableCredentialSealing gives the api_keys and provider_accounts stores
// their vault; without one, automation tokens and provider credentials cannot
// be read or written. Call once at boot, before serving.
func (r *Repo) EnableCredentialSealing(sealer CredentialSealer) {
	r.apiKeys.SetSealer(sealer)
	r.accounts.SetSealer(sealer)
}

// backfillAPIKeys seals legacy plaintext api_keys rows. Idempotent; returns the
// number of rows sealed. Requires EnableCredentialSealing.
func (r *Repo) backfillAPIKeys(ctx context.Context, batch int) (int, error) {
	return r.apiKeys.BackfillAPIKeys(ctx, batch)
}

// countUnsealedAPIKeys returns count(api_keys WHERE api_key_sealed IS NULL).
func (r *Repo) countUnsealedAPIKeys(ctx context.Context) (int64, error) {
	return r.apiKeys.CountUnsealedAPIKeys(ctx)
}

// validateAPIKeysSealedConstraint validates the api_key_sealed NOT NULL check.
func (r *Repo) validateAPIKeysSealedConstraint(ctx context.Context) error {
	return r.apiKeys.ValidateAPIKeysSealedConstraint(ctx)
}

// ListUserIDsWithProviderAccount returns every user holding an account for provider, without
// opening (decrypting) any of them.
func (r *Repo) ListUserIDsWithProviderAccount(ctx context.Context, provider string) ([]string, error) {
	return r.accounts.ListUserIDsWithProviderAccount(ctx, provider)
}
