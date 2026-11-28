// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/dblock"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/planning"
	"github.com/reliant-labs/reliant/internal/projectaccess"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflowdrafts"
)

// ==================== Chats ====================

func (r *Repo) CreateChat(ctx context.Context, chat *Chat) error {
	if chat == nil {
		return fmt.Errorf("chat cannot be nil")
	}
	return store.InsertChat(ctx, r.ORM(), chat)
}

// GetLiveChatIDForWorktree returns a non-archived chat bound to this worktree,
// or an empty string when it is unused.
func (r *Repo) GetLiveChatIDForWorktree(ctx context.Context, worktreeID string) (string, error) {
	return store.GetLiveChatIDForWorktree(ctx, r.ORM(), worktreeID)
}

// ListWorktreeIDsWithRunningChat reports which of worktreeIDs have a run going
// in them: a bound chat's run, or a bound thread's own (store.WorktreeRunActivities).
func (r *Repo) ListWorktreeIDsWithRunningChat(ctx context.Context, worktreeIDs []string) (map[string]bool, error) {
	return store.ListWorktreeIDsWithRunningChat(ctx, r.ORM(), worktreeIDs)
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

// ListArchivedChats lists the user's archived chats, a page of them when
// page.Limit is set; the bool says whether more follow.
func (r *Repo) ListArchivedChats(ctx context.Context, userID string, org *core.OrgScope, page ArchivedChatPage) ([]*ArchivedChatInfo, bool, error) {
	return store.ListArchivedChats(ctx, r.ORM(), userID, org, page)
}

// ListRuns is the cross-cutting run list, scoped to filters.UserID.
func (r *Repo) ListRuns(ctx context.Context, filters RunListFilters) ([]*RunListItem, bool, error) {
	return store.ListRuns(ctx, r.ORM(), filters)
}

// ListUnlaunchedTriggerEvents is the run log's other half: the trigger
// events that started no run, under the same filters and keyset as ListRuns.
//
// Deprecated: use store.RunListExtStore.ListUnlaunchedTriggerEvents; the run
// handler reads it through Deps.RunLists.
func (r *Repo) ListUnlaunchedTriggerEvents(ctx context.Context, filters RunListFilters) ([]*UnlaunchedTriggerEvent, bool, error) {
	return store.ListUnlaunchedTriggerEvents(ctx, r.ORM(), filters)
}

// LastRunPerWorkflow is store.LastRunPerWorkflow.
//
// Deprecated: use store.RunListExtStore.LastRunPerWorkflow.
func (r *Repo) LastRunPerWorkflow(ctx context.Context, filters RunListFilters) ([]*RunListItem, error) {
	return store.LastRunPerWorkflow(ctx, r.ORM(), filters)
}

// LastRunPerWorkflowAndOrg is the newest run of each (workflow, org) pair:
// LastRunPerWorkflow's candidates, before a caller drops the orgs a person
// may no longer see (store.LastRunPerWorkflowAndOrg).
//
// Deprecated: use store.RunListExtStore.LastRunPerWorkflowAndOrg; the run
// handler reads it through Deps.RunLists.
func (r *Repo) LastRunPerWorkflowAndOrg(ctx context.Context, filters RunListFilters) ([]*RunListItem, error) {
	return store.LastRunPerWorkflowAndOrg(ctx, r.ORM(), filters)
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
	// An LLM-only delivery (an async ask_user answer) is the model's input and
	// shown elsewhere, on the question card, so it is saved hidden.
	if displayStyle == nil && reliantv1.MessageRole(role) == reliantv1.MessageRole_MESSAGE_ROLE_USER {
		displayStyle = QueuedDisplayStyle(ctx)
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
		// A user-role message an agent sent through the human's send path
		// (send_to_run) names its sender, so it is never shown as the human's.
		origin, attributed := MessageOriginFrom(ctx)
		attributed = attributed && reliantv1.MessageRole(role) == reliantv1.MessageRole_MESSAGE_ROLE_USER
		if attributed {
			kind := origin.Kind
			msg.SenderThreadID = &origin.ThreadID
			msg.SenderKind = &kind
			if origin.Subject != "" {
				msg.SenderSubject = &origin.Subject
			}
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
		if attributed {
			updateData.Sender = NewMessageSenderPayload(origin.ThreadID, r.senderTitle(txCtx, origin.ThreadID), origin.Kind, origin.Subject)
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

// CountMessagesInChat returns the true message count for a chat, read from
// the trigger-maintained chat_thread_message_counts rather than counted.
func (r *Repo) CountMessagesInChat(ctx context.Context, chatID string) (int, error) {
	if chatID == "" {
		return 0, fmt.Errorf("chat ID cannot be empty")
	}

	return store.CountMessagesInChat(ctx, r.ORM(), chatID)
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

// ListRecentTranscriptMessagesInContextWindowBeforeSeq returns the newest
// `limit` transcript messages (every row but the DISPLAY_STYLE_HIDDEN ones) in
// a single context window strictly before beforeSeq (0 means unbounded),
// ascending, bounded in SQL.
func (r *Repo) ListRecentTranscriptMessagesInContextWindowBeforeSeq(ctx context.Context, contextWindowID string, beforeSeq int64, limit int) ([]*Message, error) {
	if contextWindowID == "" {
		return nil, fmt.Errorf("context window ID cannot be empty")
	}

	return store.ListRecentTranscriptMessagesInContextWindowBeforeSeq(ctx, r.ORM(), contextWindowID, beforeSeq, limit)
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

// ListContentBlocksByIDs reads blocks by primary key, in one query. An id no
// row holds is absent from the result.
func (r *Repo) ListContentBlocksByIDs(ctx context.Context, ids []string) ([]*MessageContentBlock, error) {
	return store.ListContentBlocksByIDs(ctx, r.ORM(), ids)
}

// ListMessagesByIDs reads messages by primary key, in one query. An id no
// row holds is absent from the result.
func (r *Repo) ListMessagesByIDs(ctx context.Context, ids []string) ([]*Message, error) {
	return store.ListMessagesByIDs(ctx, r.ORM(), ids)
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
	if status != int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
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

// ListProjectsByIDs returns the projects with these ids in one query; an id
// with no project is absent. It checks no owner: the caller decides each row.
func (r *Repo) ListProjectsByIDs(ctx context.Context, ids []string) ([]*Project, error) {
	return store.ListProjectsByIDs(ctx, r.ORM(), ids)
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
	return projectaccess.NewFromORM(r.ORM()).GetProjectWithUserCheck(ctx, id, userID)
}

// AssignUnassignedOrg gives userID's projects and chats that have no org yet
// orgID (their default org), in bounded batches, each its own transaction:
// the lazy backfill of rows written before orgs (internal/orgs ListScope).
func (r *Repo) AssignUnassignedOrg(ctx context.Context, userID, orgID string) error {
	return store.AssignUnassignedOrg(ctx, r.ORM(), userID, orgID)
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
	// Recording the checkout ends a detachment of the pair, in the same
	// transaction: a pair never has both (project_daemon_detachment.go). It
	// is the USER's choice (attach, create, clone), so it may; a checkout the
	// platform merely found goes through RecordObservedProjectDaemonInScope. The
	// catalog lock orders it against a concurrent detach.
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if err := store.LockProjectCatalog(ctx, r.ORM(), projectID); err != nil {
			return err
		}
		if err := store.RecordProjectDaemonInstalled(ctx, r.ORM(), projectID, daemonID, path, defaultBranch); err != nil {
			return err
		}
		return store.ClearProjectDaemonDetachment(ctx, r.ORM(), projectID, daemonID)
	})
}

// RecordObservedProjectDaemonInScope records a checkout the platform found
// on the machine (the connect reconcile, the plain-folder heal, a discovered
// folder, a pool member's restore), never one the user chose, for a machine
// that holds only scope's projects (its org's, or its owner's default org's
// when its org is not known: authz.MachineScopeFor; nil, every org's, only in
// a deployment with no orgs).
//
// Two things win over the write, each re-read in its transaction:
//   - a detachment of the pair: it records nothing and reports false, and it
//     never clears the detachment. The check runs under the catalog lock
//     DetachProjectFromDaemon holds, so a detach that commits while the caller
//     was probing the machine is seen here rather than undone;
//   - the project's org: the project row is locked FOR SHARE — a project's
//     org move takes it FOR NO KEY UPDATE, so the two serialize — and nothing
//     is recorded unless the project's org NOW is in scope. A project moved
//     out of the machine's org while the caller probed or cloned is never
//     recorded on it.
//
// A project gone since is false, not an error. There is deliberately no
// unscoped form: every caller records for one machine, whose scope it has.
func (r *Repo) RecordObservedProjectDaemonInScope(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, scope *core.OrgScope) (bool, error) {
	if projectID == "" {
		return false, fmt.Errorf("project ID cannot be empty")
	}
	if daemonID == "" {
		return false, fmt.Errorf("daemon ID cannot be empty")
	}
	if path == "" {
		return false, fmt.Errorf("path cannot be empty")
	}
	recorded := false
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		recorded = false
		inScope, err := r.lockProjectInScope(ctx, projectID, scope)
		if err != nil || !inScope {
			return err
		}
		detached, err := store.ProjectDaemonDetached(ctx, r.ORM(), projectID, daemonID)
		if err != nil || detached {
			return err
		}
		if err := store.RecordProjectDaemonInstalled(ctx, r.ORM(), projectID, daemonID, path, defaultBranch); err != nil {
			return err
		}
		recorded = true
		return nil
	})
	return recorded, err
}

// UpsertQueuedProjectDaemon records a clone that is queued but has not run,
// and the nested repositories (nestedRepoIDs, the project's repos below its
// root) to clone into it once it lands, in one transaction.
func (r *Repo) UpsertQueuedProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, requestID string, nestedRepoIDs []string) error {
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
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if err := store.LockProjectCatalog(ctx, r.ORM(), projectID); err != nil {
			return err
		}
		if err := store.RecordProjectDaemonQueued(ctx, r.ORM(), projectID, daemonID, path, defaultBranch, requestID); err != nil {
			return err
		}
		if err := store.PlanProjectDaemonRepos(ctx, r.ORM(), projectID, daemonID, nestedRepoIDs); err != nil {
			return err
		}
		return store.ClearProjectDaemonDetachment(ctx, r.ORM(), projectID, daemonID)
	})
}

// MarkProjectDaemonInstalledInScope settles the machine's clone of the
// project as installed, from evidence the machine gave (its filesystem
// announcement, or a directory at the row's own path for a clone whose outcome
// was lost), for a machine that holds only scope's projects (nil: every
// org's). It reports whether the checkout is installed now: false when there
// is no row, or when the project is gone or no longer in scope.
//
// Like RecordObservedProjectDaemonInScope, it re-reads the project's org
// under the catalog lock and the project row FOR SHARE, so a move out of the
// machine's org that committed while the caller probed the machine is seen
// and nothing is settled on it. A detached pair has no row, so it settles
// nothing. There is deliberately no unscoped form.
func (r *Repo) MarkProjectDaemonInstalledInScope(ctx context.Context, projectID, daemonID string, scope *core.OrgScope) (bool, error) {
	if projectID == "" || daemonID == "" {
		return false, fmt.Errorf("project ID and daemon ID cannot be empty")
	}
	installed := false
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		installed = false
		inScope, err := r.lockProjectInScope(ctx, projectID, scope)
		if err != nil || !inScope {
			return err
		}
		if err := store.MarkProjectDaemonInstalled(ctx, r.ORM(), projectID, daemonID); err != nil {
			return err
		}
		installed, err = store.ProjectDaemonInstalled(ctx, r.ORM(), projectID, daemonID)
		return err
	})
	return installed, err
}

// lockProjectInScope takes, inside the caller's transaction, the locks every
// machine-observed checkout or config write holds — the project's catalog
// lock, then its row FOR SHARE — and reports whether the write may land: the
// project still exists, is not scheduled for deletion, and its org, read
// under that lock, is in scope.
//
// The row lock conflicts with a project's org move (FOR NO KEY UPDATE) and a
// deletion schedule (FOR UPDATE), so either is committed and seen here, or
// waits for this write. Neither takes the catalog lock — their order is the
// owner's org lock, then the project row (store/project_schedule.go) — and
// nothing that holds a project row takes it after, so the order cannot
// deadlock. A project scheduled for deletion gets nothing new from its
// machines: no checkout recorded or settled, no config synced
// (project_delete_admission covers chats, workspaces, runs and triggers in
// the database; these writes are refused here). A restore clears
// delete_after, and the next connect records and syncs again.
func (r *Repo) lockProjectInScope(ctx context.Context, projectID string, scope *core.OrgScope) (bool, error) {
	if err := store.LockProjectCatalog(ctx, r.ORM(), projectID); err != nil {
		return false, err
	}
	org, scheduled, found, err := store.LockProjectOrgForShare(ctx, r.ORM(), projectID)
	if err != nil || !found || scheduled {
		return false, err
	}
	return scope.Includes(org), nil
}

// FailProjectDaemonInstallInScope settles the machine's clone of the project
// that is still installing as failed — one whose outcome was lost and whose
// path is empty on the machine — for a machine that holds only scope's
// projects (nil: every org's). It reports whether it marked the row failed.
// The org is re-read under the same locks as MarkProjectDaemonInstalledInScope:
// a project moved out of the machine's org meanwhile is not the machine's to
// settle (the machine-org cleanup detaches its row instead).
func (r *Repo) FailProjectDaemonInstallInScope(ctx context.Context, projectID, daemonID, installErr string, scope *core.OrgScope) (bool, error) {
	if projectID == "" || daemonID == "" {
		return false, fmt.Errorf("project ID and daemon ID cannot be empty")
	}
	failed := false
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		failed = false
		inScope, err := r.lockProjectInScope(ctx, projectID, scope)
		if err != nil || !inScope {
			return err
		}
		failed, err = store.FailProjectDaemonInstall(ctx, r.ORM(), projectID, daemonID, installErr)
		return err
	})
	return failed, err
}

// SettleProjectDaemonFromNestedReposInScope settles the machine's checkout
// of the project from its nested repositories' outcomes, once none is left
// to clone: failed with installErr when installErr is set (one did not
// clone; from installing or installed, see
// store.FailProjectDaemonFromNestedRepos), else installed (every one
// cloned). Both are fenced on the nested plan in the same statement, so a
// set-up planned again meanwhile is never settled by the old plan's outcome,
// and a checkout with a failed nested repository never reads installed. The
// org is re-read under the same locks as MarkProjectDaemonInstalledInScope.
// It reports whether the row is in the settled state now: installed for a
// success, marked failed by this call for a failure.
func (r *Repo) SettleProjectDaemonFromNestedReposInScope(ctx context.Context, projectID, daemonID, installErr string, scope *core.OrgScope) (bool, error) {
	if projectID == "" || daemonID == "" {
		return false, fmt.Errorf("project ID and daemon ID cannot be empty")
	}
	settled := false
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		settled = false
		inScope, err := r.lockProjectInScope(ctx, projectID, scope)
		if err != nil || !inScope {
			return err
		}
		if installErr != "" {
			settled, err = store.FailProjectDaemonFromNestedRepos(ctx, r.ORM(), projectID, daemonID, installErr)
			return err
		}
		if err := store.MarkProjectDaemonInstalledFromNestedRepos(ctx, r.ORM(), projectID, daemonID); err != nil {
			return err
		}
		settled, err = store.ProjectDaemonInstalled(ctx, r.ORM(), projectID, daemonID)
		return err
	})
	return settled, err
}

// RecordFailedProjectDaemonRestoreInScope records that putting a pool
// member's checkout of the project back failed, with installErr as the
// reason, so its install state and placement say so instead of waiting on it
// forever. The restore clones for minutes before it gets here, so, like
// RecordObservedProjectDaemonInScope, it writes nothing when the project
// moved out of scope (nil: every org's) or the user detached it from the
// machine meanwhile — and it never clears a detachment: a restore is the
// platform's doing, not the user's. It reports whether it recorded the
// failure.
func (r *Repo) RecordFailedProjectDaemonRestoreInScope(ctx context.Context, projectID, daemonID, path string, defaultBranch *string, installErr string, scope *core.OrgScope) (bool, error) {
	if projectID == "" || daemonID == "" || path == "" {
		return false, fmt.Errorf("project ID, daemon ID and path cannot be empty")
	}
	recorded := false
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		recorded = false
		inScope, err := r.lockProjectInScope(ctx, projectID, scope)
		if err != nil || !inScope {
			return err
		}
		detached, err := store.ProjectDaemonDetached(ctx, r.ORM(), projectID, daemonID)
		if err != nil || detached {
			return err
		}
		// Queued and resolved at once, under a request id of its own: a
		// late outcome of an earlier clone cannot overwrite the failure.
		requestID := uuid.NewString()
		if err := store.RecordProjectDaemonQueued(ctx, r.ORM(), projectID, daemonID, path, defaultBranch, requestID); err != nil {
			return err
		}
		if err := store.ResolveQueuedProjectDaemon(ctx, r.ORM(), requestID, installErr); err != nil {
			return err
		}
		recorded = true
		return nil
	})
	return recorded, err
}

func (r *Repo) ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return store.ListProjectDaemonsForProject(ctx, r.ORM(), projectID)
}

// ListProjectDaemonsForProjects returns where every project in projectIDs is
// installed, by project and then oldest clone first: one read for a list of
// projects.
func (r *Repo) ListProjectDaemonsForProjects(ctx context.Context, projectIDs []string) ([]*core.ProjectDaemon, error) {
	return store.ListProjectDaemonsForProjects(ctx, r.ORM(), projectIDs)
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

// ErrDaemonRequiresBoundToken is returned when an unbound write (a daemon
// connecting with a login token) names an id its owner's row reserves for
// daemon-bound credentials (Daemon.RequiresBoundToken).
var ErrDaemonRequiresBoundToken = svcerr.PermissionDenied("daemon id is reserved for daemon-bound credentials")

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
	// The conflict branch only fires for the row's own owner, and for an
	// unbound write only on a row not reserved for bound credentials, so
	// either refusal writes nothing — in a single statement, with no
	// read-then-write window. The read below only names which refusal it
	// was; it decides nothing.
	if !wrote {
		if existing, getErr := store.GetDaemon(ctx, r.ORM(), daemon.ID); getErr == nil &&
			existing.UserID == daemon.UserID && existing.RequiresBoundToken && !daemon.RequiresBoundToken {
			return fmt.Errorf("%w: daemon %s", ErrDaemonRequiresBoundToken, daemon.ID)
		}
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

// SetDefaultDaemon makes daemonID the user's default machine, replacing any
// previous one; "" clears it. A machine the user does not own (or that does
// not exist) is svcerr.NotFound, and nothing changes.
func (r *Repo) SetDefaultDaemon(ctx context.Context, userID, daemonID string) error {
	if userID == "" {
		return fmt.Errorf("user ID cannot be empty")
	}
	// ReadCommitted: store.SetDefaultDaemon locks the user's rows first, and
	// that lock is what makes the clear-then-set correct.
	return r.RunTx(ctx, orm.ReadCommitted, func(txCtx context.Context) error {
		owned, err := store.SetDefaultDaemon(txCtx, r.ORM(), userID, daemonID)
		if err != nil {
			return fmt.Errorf("failed to set default daemon for user %s: %w", userID, err)
		}
		if !owned {
			return svcerr.NotFound("daemon")
		}
		return nil
	})
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

// TryAdvisoryLock takes dblock's session-level lock on key from the session
// pool (SessionPool).
//
// Only worktreesweep still reaches it through the repository, as its Store's
// leader election; every other job takes its lock from a dblock.Locker. It goes
// when the sweeper takes a Locker of its own.
func (r *Repo) TryAdvisoryLock(ctx context.Context, key int64) (func(), bool, error) {
	locker, err := dblock.NewInstrumented(dblock.Deps{Sessions: r.sessionPool()})
	if err != nil {
		return nil, false, err
	}
	return locker.TryLock(ctx, dblock.Key(key))
}

// SessionPool is the pool a caller that needs a Postgres session to itself
// takes its connection from: DatabaseConfig.DirectURL's pool when the main pool
// is transaction-pooled, the main pool otherwise.
func (r *Repo) SessionPool() *sql.DB { return r.sessionPool() }

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

// GetProjectWorkflowsJSON reads one column of the project config record: the
// synced .reliant/workflows. It returns svcerr.NotFound when the
// project has no record. Prefer it (and GetProjectPresetsJSON) to
// store.GetProjectConfigRecord whenever that column is all a caller needs: the
// full row carries every indexed skill body and can run to tens of MB.
//
// A value this Repo read before is served again while the record is
// unchanged (catalogColumnCache).
func (r *Repo) GetProjectWorkflowsJSON(ctx context.Context, projectID string) (*string, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return r.projectConfigColumn(ctx, "project.workflows", projectID, store.GetProjectWorkflowsJSONVersioned)
}

// GetProjectPresetsJSON reads one column of the project config record: the
// synced .reliant/presets. It returns svcerr.NotFound when the
// project has no record. Cached as GetProjectWorkflowsJSON is.
func (r *Repo) GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return r.projectConfigColumn(ctx, "project.presets", projectID, store.GetProjectPresetsJSONVersioned)
}

// projectConfigColumn reads one JSON column of projectID's config record
// through the catalog column cache, svcerr.NotFound when there is none.
func (r *Repo) projectConfigColumn(ctx context.Context, column, projectID string,
	versioned func(ctx context.Context, db orm.Context, projectID string) (*string, string, bool, error),
) (*string, error) {
	value, found, err := r.catalogColumns().read(ctx, catalogColumnKey{column: column, projectID: projectID}, catalogColumnReads{
		version: func(ctx context.Context) (string, bool, error) {
			return store.ProjectConfigVersion(ctx, r.ORM(), projectID)
		},
		versioned: func(ctx context.Context) (*string, string, bool, error) {
			return versioned(ctx, r.ORM(), projectID)
		},
	}, r.fillsCatalogColumns(ctx))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, store.MissingProjectConfig()
	}
	return value, nil
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
	if worktree.IsMain || worktree.DaemonID == nil || *worktree.DaemonID == "" {
		return store.InsertWorktree(ctx, r.ORM(), worktree)
	}
	// A branch workspace is a git worktree of the project's checkout on its
	// machine: inserted under the shared side of the lock a detach of that
	// checkout holds (DetachProjectFromDaemon), so it lands before the
	// detach's workspace guard reads or after the detach committed.
	return r.RunTx(ctx, orm.ReadCommitted, func(txCtx context.Context) error {
		if err := store.LockProjectCheckout(txCtx, r.ORM(), worktree.ProjectID, *worktree.DaemonID, true); err != nil {
			return err
		}
		// And not while a remove or reclone of that checkout may still be
		// deleting it (BeginCheckoutTeardown takes the exclusive side).
		pending, err := store.PendingCheckoutTeardown(txCtx, r.ORM(), worktree.ProjectID, *worktree.DaemonID, false)
		if err != nil {
			return err
		}
		if pending != nil {
			return &CheckoutTeardownPendingError{Teardown: pending}
		}
		return store.InsertWorktree(txCtx, r.ORM(), worktree)
	})
}

func (r *Repo) GetWorktree(ctx context.Context, id string) (*Worktree, error) {
	if id == "" {
		return nil, fmt.Errorf("worktree ID cannot be empty")
	}
	return store.GetWorktree(ctx, r.ORM(), id)
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

// ListFailedWorktreesForDaemon returns daemonID's FAILED workspaces created
// since `since` whose leftover checkouts are not yet known to be gone, newest
// first, at most limit (store.ListFailedWorktreesForDaemon).
func (r *Repo) ListFailedWorktreesForDaemon(ctx context.Context, daemonID string, since time.Time, limit int) ([]*Worktree, error) {
	if daemonID == "" {
		return nil, nil
	}
	return store.ListFailedWorktreesForDaemon(ctx, r.ORM(), daemonID, since, limit)
}

func (r *Repo) ListWorktrees(ctx context.Context, filters WorktreeFilters) ([]*Worktree, error) {
	return store.ListWorktrees(ctx, r.ORM(), filters)
}

func (r *Repo) ListWorktreesByIDs(ctx context.Context, ids []string) ([]*Worktree, error) {
	return store.ListWorktreesByIDs(ctx, r.ORM(), ids)
}

// ListMainWorktreesForUser returns the main workspace of each of the user's
// projects, or of each project named that the user owns when projects is
// non-empty.
func (r *Repo) ListMainWorktreesForUser(ctx context.Context, userID string, projects []string) ([]*Worktree, error) {
	return store.ListMainWorktreesForUser(ctx, r.ORM(), userID, projects)
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

// MergeWorktreeCleanupMetadata is a read-modify-write of one row's
// cleanup_metadata under SELECT ... FOR UPDATE.
func (r *Repo) MergeWorktreeCleanupMetadata(ctx context.Context, id string, fn func(m *CleanupMetadata, archived bool) bool) error {
	if id == "" {
		return fmt.Errorf("worktree ID cannot be empty")
	}
	return store.MergeWorktreeCleanupMetadata(ctx, r.ORM(), id, fn)
}

// AdoptWorktreeDaemon records the daemon that found a workspace's directory.
//
// Deprecated: use store.WorktreeExtStore.AdoptWorktreeDaemon; the sweep reads
// it there, and only a test of the activities still calls this.
func (r *Repo) AdoptWorktreeDaemon(ctx context.Context, id, daemonID string) (string, error) {
	if id == "" || daemonID == "" {
		return "", fmt.Errorf("worktree and daemon IDs cannot be empty")
	}
	return store.AdoptWorktreeDaemon(ctx, r.ORM(), id, daemonID)
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

// SetRepoForgeApps records the forge apps a daemon found in a repo
// (repo_forge_apps). complete (the daemon's walk covered the repo) replaces
// the set and stamps repos.forge_apps_scanned_at; a partial walk only adds and
// renames, since what it did not reach may still be there. A repo deleted
// meanwhile is a no-op.
func (r *Repo) SetRepoForgeApps(ctx context.Context, repoID string, apps []core.RepoForgeApp, complete bool) error {
	if repoID == "" {
		return fmt.Errorf("repo ID cannot be empty")
	}
	// ReadCommitted: the repo's row lock, taken first, is what serializes
	// two reports for one repo.
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return store.SetRepoForgeApps(ctx, r.ORM(), repoID, apps, complete)
	})
}

// ListRepoForgeAppsByProject returns every forge app recorded in the
// project's repos, ordered by repo and subpath.
func (r *Repo) ListRepoForgeAppsByProject(ctx context.Context, projectID string) ([]core.RepoForgeApp, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return store.ListRepoForgeAppsByProject(ctx, r.ORM(), projectID)
}

// ListReposHoldingForgeApp returns the repos of projectIDs in which a forge
// app named name is recorded (repo_forge_apps), ordered by project and path.
func (r *Repo) ListReposHoldingForgeApp(ctx context.Context, name string, projectIDs []string) ([]*core.Repo, error) {
	return store.ListReposHoldingForgeApp(ctx, r.ORM(), name, projectIDs)
}

// ListReposByProjects returns the repos of every project in projectIDs, by
// project and relative path: one read for a list of projects.
func (r *Repo) ListReposByProjects(ctx context.Context, projectIDs []string) ([]*core.Repo, error) {
	return store.ListReposByProjects(ctx, r.ORM(), projectIDs)
}

// ListRepoForgeAppsByProjects returns every forge app recorded in the repos
// of every project in projectIDs, ordered by repo and subpath.
func (r *Repo) ListRepoForgeAppsByProjects(ctx context.Context, projectIDs []string) ([]core.RepoForgeApp, error) {
	return store.ListRepoForgeAppsByProjects(ctx, r.ORM(), projectIDs)
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

func (r *Repo) SetString(ctx context.Context, userID string, projectID *string, key, value string) error {
	return r.upsertSetting(ctx, userID, projectID, key, value, "string")
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

// ==================== Visibility Methods ====================

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

// ListHiddenItemDefaults returns all items that are hidden by default.
func (r *Repo) ListHiddenItemDefaults(ctx context.Context, itemType int32) ([]string, error) {
	if itemType != 1 && itemType != 2 { // HIDDEN_ITEM_TYPE_WORKFLOW=1, HIDDEN_ITEM_TYPE_PRESET=2
		return nil, fmt.Errorf("item_type must be WORKFLOW (1) or PRESET (2)")
	}

	return store.ListHiddenItemDefaults(ctx, r.ORM(), itemType)
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

func (r *Repo) ListPlansByChatID(ctx context.Context, chatID string) ([]*Plan, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	return store.ListPlansByChatID(ctx, r.ORM(), chatID)
}

// CopyChatPlans gives toThreadID a pending copy of every plan of fromChatID
// and of their tasks, in one statement (store.CopyChatPlans). A branch
// starts with its source chat's plans this way.
func (r *Repo) CopyChatPlans(ctx context.Context, fromChatID, toThreadID string) error {
	return store.CopyChatPlans(ctx, r.ORM(), fromChatID, toThreadID, int32(PlanStatusPending), int32(TaskStatusPending))
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

func (r *Repo) ListTasksByPlan(ctx context.Context, planID string) ([]*Task, error) {
	if planID == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}

	return store.ListTasksByPlan(ctx, r.ORM(), planID)
}

// ==================== Task Dependencies ====================

// ==================== Planning (deprecated forwarders) ====================
//
// The plan, task and dependency calls of the agent planning tools live in
// internal/planning. These stay as forwarding wrappers so code that calls them
// through the Repo keeps compiling within v1.

func (r *Repo) planning() (planning.Service, error) {
	r.planningOnce.Do(func() {
		r.planningSvc, r.planningErr = planning.NewInstrumented(planning.Deps{
			Plans:        store.NewPlanExtStore(r.ORM()),
			Tasks:        store.NewTaskExtStore(r.ORM()),
			Dependencies: store.NewTaskDependencyExtStore(r.ORM()),
		})
	})
	return r.planningSvc, r.planningErr
}

// GetPlanByThreadID returns the plan a thread works on.
//
// Deprecated: use planning.Service.GetPlanByThreadID.
func (r *Repo) GetPlanByThreadID(ctx context.Context, threadID string) (*Plan, error) {
	svc, err := r.planning()
	if err != nil {
		return nil, err
	}
	return svc.GetPlanByThreadID(ctx, threadID)
}

// UpdatePlan rewrites a plan row.
//
// Deprecated: use planning.Service.UpdatePlan.
func (r *Repo) UpdatePlan(ctx context.Context, plan *Plan) error {
	svc, err := r.planning()
	if err != nil {
		return err
	}
	return svc.UpdatePlan(ctx, plan)
}

// GetTaskByPosition returns the plan's task at a 0-indexed position.
//
// Deprecated: use planning.Service.GetTaskByPosition.
func (r *Repo) GetTaskByPosition(ctx context.Context, planID string, position int) (*Task, error) {
	svc, err := r.planning()
	if err != nil {
		return nil, err
	}
	return svc.GetTaskByPosition(ctx, planID, position)
}

// GetTaskStatsByPlan counts the plan's tasks by status.
//
// Deprecated: use planning.Service.GetTaskStatsByPlan.
func (r *Repo) GetTaskStatsByPlan(ctx context.Context, planID string) (*TaskStats, error) {
	svc, err := r.planning()
	if err != nil {
		return nil, err
	}
	return svc.GetTaskStatsByPlan(ctx, planID)
}

// UpdateTask rewrites a task row.
//
// Deprecated: use planning.Service.UpdateTask.
func (r *Repo) UpdateTask(ctx context.Context, task *Task) error {
	svc, err := r.planning()
	if err != nil {
		return err
	}
	return svc.UpdateTask(ctx, task)
}

// CreateTaskDependency links two tasks.
//
// Deprecated: use planning.Service.CreateTaskDependency.
func (r *Repo) CreateTaskDependency(ctx context.Context, dep *TaskDependency) error {
	svc, err := r.planning()
	if err != nil {
		return err
	}
	return svc.CreateTaskDependency(ctx, dep)
}

// ListDependenciesByPlan lists every dependency among the plan's tasks.
//
// Deprecated: use planning.Service.ListDependenciesByPlan.
func (r *Repo) ListDependenciesByPlan(ctx context.Context, planID string) ([]*TaskDependency, error) {
	svc, err := r.planning()
	if err != nil {
		return nil, err
	}
	return svc.ListDependenciesByPlan(ctx, planID)
}

// DeleteTaskDependencyByPair removes the dependency of one type between two tasks.
//
// Deprecated: use planning.Service.DeleteTaskDependencyByPair.
func (r *Repo) DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error {
	svc, err := r.planning()
	if err != nil {
		return err
	}
	return svc.DeleteTaskDependencyByPair(ctx, fromTaskID, toTaskID, depType)
}

// ==================== Background Processes ====================

func (r *Repo) GetBackgroundProcess(ctx context.Context, id string) (*BackgroundProcess, error) {
	if id == "" {
		return nil, fmt.Errorf("process ID cannot be empty")
	}
	return store.GetBackgroundProcess(ctx, r.ORM(), id)
}

func (r *Repo) UpdateBackgroundProcessStatus(ctx context.Context, id string, status BackgroundProcessStatus, exitCode *int, endedAt *time.Time) error {
	if id == "" {
		return fmt.Errorf("process ID cannot be empty")
	}
	return store.SetBackgroundProcessStatus(ctx, r.ORM(), id, status, exitCode, endedAt)
}

// ==================== Background Process Output ====================

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
		return r.getThreadTokenCountDirect(ctx, thread, maxSeq)
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

// getThreadTokenCountDirect queries the thread's token count directly.
// Used as fallback when Thread record doesn't exist.
func (r *Repo) getThreadTokenCountDirect(ctx context.Context, thread string, maxSeq *int64) (int64, error) {
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

	// READ COMMITTED: the write is one INSERT … ON CONFLICT DO NOTHING (the
	// activity emit runs after commit), and its admission triggers are
	// row-lock protocols that bump the chat's worktree rows. SERIALIZABLE
	// failed every insert that waited on another run's admission in the same
	// chat with SQLSTATE 40001 (threads.Service.CreateWorkflowWithThread).
	return r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		if err := store.InsertWorkflowIfAbsent(txCtx, r.ORM(), workflow); err != nil {
			return err
		}
		if workflow.ChatID != "" {
			return r.emitChatActivityIfChanged(txCtx, workflow.ChatID)
		}
		return nil
	})
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
		wf, err := store.LoadWorkflow(txCtx, r.ORM(), id)
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

// CancelStoppedSpawn records that a spawn was told to stop: its workflow row
// moves from active or paused (an explicit stop overrides a pause) to
// cancelled, and its descendants and the threads of its whole subtree end with
// it, in ONE commit. It reports whether the row moved; a spawn that already
// settled on its own is left as it ended.
//
// The thread half is what makes this one call rather than a bare CAS. The
// spawn's own loop cancels its thread when it unwinds, seconds later; a row
// written cancelled before that left a terminal workflow over a running
// thread, which the reconciler's ReapOrphanedThreads repaired and paged on as
// a missed cascade (prod, 2026-10-30..11-04: every reap was a stopped spawn,
// its thread reaped 1-60s before the loop's own write).
//
// The subtree's still-queued mail is resolved in the same commit, for the
// same reason: a stopped agent takes no further turn, so what it had not read
// is undelivered as of the stop. Left queued under a cancelled thread, the
// reconciler's orphaned-mailbox sweep found it and reported it at ERROR as a
// broken loop invariant (reliant #2394: three spawn_send rows to a sub-agent
// parked on a question, which its parent then stopped). It is the stop's
// expected outcome, recorded by the rows themselves.
func (r *Repo) CancelStoppedSpawn(ctx context.Context, workflowID string) (bool, error) {
	if workflowID == "" {
		return false, fmt.Errorf("workflow ID cannot be empty")
	}
	var swapped bool
	err := r.RunTx(ctx, workflowStatusWrite, func(txCtx context.Context) error {
		swapped = false
		for _, from := range []WorkflowStatus{Active(), Paused()} {
			ok, err := r.CompareAndSwapWorkflowStatus(txCtx, workflowID, Cancelled(), from)
			if err != nil {
				return err
			}
			if ok {
				swapped = true
				break
			}
		}
		if !swapped {
			return nil
		}
		if err := r.CascadeTerminalStatusToDescendants(txCtx, workflowID, StopReasonCancelled); err != nil {
			return fmt.Errorf("cascade cancel to the descendants of spawn %s: %w", workflowID, err)
		}
		if err := r.CascadeTerminalStatusToThreadSubtree(txCtx, workflowID, StopReasonCancelled); err != nil {
			return fmt.Errorf("cascade cancel to the threads of spawn %s: %w", workflowID, err)
		}
		resolved, err := store.MarkQueuedAgentMessagesUndeliveredForWorkflowSubtree(txCtx, r.ORM(), workflowID)
		if err != nil {
			return fmt.Errorf("resolve the mailboxes of stopped spawn %s: %w", workflowID, err)
		}
		if resolved > 0 {
			if wf, err := store.LoadWorkflow(txCtx, r.ORM(), workflowID); err == nil && wf != nil && wf.ChatID != "" {
				r.announceAgentMailboxChanged(txCtx, wf.ChatID)
			}
		}
		return nil
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
		wf, err := store.LoadWorkflow(txCtx, r.ORM(), id)
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
		wf, err := store.LoadWorkflow(txCtx, r.ORM(), id)
		if err != nil {
			return err
		}
		if wf.ChatID == "" {
			return nil // no chat association
		}
		return r.emitChatActivityIfChanged(txCtx, wf.ChatID)
	})
}

// HandledNodeFailure is a node of an ended run whose last attempt failed.
type HandledNodeFailure = store.HandledNodeFailure

// EnsureWorkflowRunning is a lightweight idempotent check: if the workflow
// is not already Running, transition it to Running and emit an activity event.
// Designed to be called from the activity wrapper so that any Temporal activity
// execution automatically flips the chat into the active state.
// Returns quickly with no DB writes when the workflow is already Running.
func (r *Repo) EnsureWorkflowRunning(ctx context.Context, workflowID, chatID string) {
	if workflowID == "" {
		return
	}
	wf, err := store.LoadWorkflow(ctx, r.ORM(), workflowID)
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

// OrgRead is the org a write's decision read from one row (store.OrgRead):
// the write re-checks it under its owner's org lock.
type OrgRead = store.OrgRead

// ErrOrgChanged refuses a write whose org decision a backfill changed before
// it committed (store.ErrOrgChanged).
var ErrOrgChanged = store.ErrOrgChanged

// LockUserOrgs takes userID's org lock for the rest of the surrounding
// transaction and re-checks each read (store.LockUserOrgs). Call it inside a
// transaction, before any row lock of it.
func (r *Repo) LockUserOrgs(ctx context.Context, userID string, reads ...OrgRead) error {
	return store.LockUserOrgs(ctx, r.ORM(), userID, reads...)
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

// UpdateTriggerEventOutcome is store.UpdateTriggerEventOutcome.
//
// Deprecated: use store.TriggerEventExtStore.UpdateTriggerEventOutcome; a launch reads the
// ledger through launch.Deps.TriggerEvents.
func (r *Repo) UpdateTriggerEventOutcome(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string, chatID *string) error {
	if id == "" {
		return fmt.Errorf("trigger event ID cannot be empty")
	}
	return store.UpdateTriggerEventOutcome(ctx, r.ORM(), id, outcome, detail, chatID)
}

// UpdateTriggerEventPayload is store.UpdateTriggerEventPayload.
//
// Deprecated: use store.TriggerEventExtStore.UpdateTriggerEventPayload; a launch reads the
// ledger through launch.Deps.TriggerEvents.
func (r *Repo) UpdateTriggerEventPayload(ctx context.Context, id string, payload map[string]any) error {
	if id == "" {
		return fmt.Errorf("trigger event ID cannot be empty")
	}
	return store.UpdateTriggerEventPayload(ctx, r.ORM(), id, payload)
}

// ClaimRunEvents leases up to max undispatched events until leaseUntil.
//
// Deprecated: use store.RunEventExtStore.ClaimRunEvents; the relay reads it
// there, and only tests still call this.
func (r *Repo) ClaimRunEvents(ctx context.Context, now, leaseUntil time.Time, max int) ([]*core.RunEvent, error) {
	if max <= 0 {
		return nil, nil
	}
	return store.ClaimRunEvents(ctx, r.ORM(), now, leaseUntil, max)
}

// FiringsSinceLastSuccess returns each trigger's firings since its newest
// success, newest first, capped at perTrigger: the current failure episode.
// The events carry no body (payload, sender, origin): an episode is counted.
//
// Deprecated: use store.TriggerEventExtStore.FiringsSinceLastSuccess.
func (r *Repo) FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.FiringsSinceLastSuccess(ctx, r.ORM(), userID, triggerIDs, perTrigger)
}

// PriorTriggerFailures returns, for each triggerIDs[i], when its newest failed
// firing in [before[i]-window, before[i]) occurred, nil when none
// (store.PriorTriggerFailures).
func (r *Repo) PriorTriggerFailures(ctx context.Context, userID string, triggerIDs []string, before []time.Time, window time.Duration) ([]*time.Time, error) {
	return store.PriorTriggerFailures(ctx, r.ORM(), userID, triggerIDs, before, window)
}

// ClaimPendingTriggerEvent is store.ClaimPendingTriggerEvent.
//
// Deprecated: use store.TriggerEventExtStore.ClaimPendingTriggerEvent; a launch reads the
// ledger through launch.Deps.TriggerEvents.
func (r *Repo) ClaimPendingTriggerEvent(ctx context.Context, id string, payload map[string]any, def *core.TriggerEventDefinition) (bool, error) {
	return store.ClaimPendingTriggerEvent(ctx, r.ORM(), id, payload, def)
}

// ==================== Step Executions ====================

func (r *Repo) CreateStepExecution(ctx context.Context, exec *StepExecution) error {
	if exec == nil {
		return fmt.Errorf("step execution cannot be nil")
	}

	return store.InsertStepExecution(ctx, r.ORM(), exec)
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

// ListStepExecutionRecordsForChat returns full step rows (inputs, output,
// error), newest first, optionally scoped to one node and what ran inside it.
func (r *Repo) ListStepExecutionRecordsForChat(ctx context.Context, chatID, nodePath string, limit int) ([]*StepExecution, error) {
	return store.ListStepExecutionRecordsForChat(ctx, r.ORM(), chatID, nodePath, limit)
}

func (r *Repo) ListWorkflowsByStatus(ctx context.Context, status WorkflowStatus) ([]*Workflow, error) {
	return store.ListWorkflowsByStatus(ctx, r.ORM(), status)
}

// ==================== Workflow Checkpoints ====================
// Position truth for resume-at-position: last top-level node entered (and
// loop iteration for loop nodes) per workflow ID.

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

// ResolveThreadAmbientMemoryExclusion fails closed on persistence errors.
func (r *Repo) ResolveThreadAmbientMemoryExclusion(ctx context.Context, chatID, threadID string, requested bool) (bool, error) {
	return store.ResolveThreadAmbientMemoryExclusion(ctx, r.ORM(), chatID, threadID, requested)
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
// User-owned workflows available across all projects. Every write and every
// scoped read lives in internal/workflowdrafts; the methods below are the few
// that code outside this package's migration still calls through the Repo.

// ErrWorkflowSlugTaken reports that the owner already has a workflow with the
// slug (workflowdrafts.ErrSlugTaken).
var ErrWorkflowSlugTaken = workflowdrafts.ErrSlugTaken

// ErrWorkflowDraftVersionConflict reports that a draft was written since the
// version the caller read (workflowdrafts.ErrVersionConflict).
var ErrWorkflowDraftVersionConflict = workflowdrafts.ErrVersionConflict

// WorkflowDraftsBySlug is one user's drafts of a set of slugs
// (workflowdrafts.DraftsBySlug).
type WorkflowDraftsBySlug = workflowdrafts.DraftsBySlug

// workflowDrafts is the drafts package over this Repo's database and
// transactions: the Repo is its TxRunner, OrgLocks and UserUpdates.
func (r *Repo) workflowDrafts() (workflowdrafts.Service, error) {
	r.draftsOnce.Do(func() {
		r.drafts, r.draftsErr = workflowdrafts.NewInstrumented(workflowdrafts.Deps{
			Tx:      r,
			Locks:   r,
			Updates: r,
			Drafts:  store.NewWorkflowDraftExtStore(r.ORM()),
		})
	})
	return r.drafts, r.draftsErr
}

func (r *Repo) CreateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error {
	svc, err := r.workflowDrafts()
	if err != nil {
		return err
	}
	return svc.CreateWorkflowDraft(ctx, draft)
}

func (r *Repo) UpdateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error {
	svc, err := r.workflowDrafts()
	if err != nil {
		return err
	}
	return svc.UpdateWorkflowDraft(ctx, draft)
}

func (r *Repo) GetWorkflowDraft(ctx context.Context, id string) (*WorkflowDraft, error) {
	svc, err := r.workflowDrafts()
	if err != nil {
		return nil, err
	}
	return svc.GetWorkflowDraft(ctx, id)
}

func (r *Repo) GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error) {
	svc, err := r.workflowDrafts()
	if err != nil {
		return nil, err
	}
	return svc.GetWorkflowDraftBySlug(ctx, userID, slug)
}

func (r *Repo) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error) {
	svc, err := r.workflowDrafts()
	if err != nil {
		return nil, err
	}
	return svc.GetUsableWorkflowBySlug(ctx, userID, slug)
}

func (r *Repo) GetWorkflowDraftsBySlugs(ctx context.Context, userID string, slugs []string) (*WorkflowDraftsBySlug, error) {
	svc, err := r.workflowDrafts()
	if err != nil {
		return nil, err
	}
	return svc.GetWorkflowDraftsBySlugs(ctx, userID, slugs)
}

// Deprecated: use workflowdrafts.Service.GetWorkflowDraftByName. Kept as a
// forwarder: the v1 contract removes an exported method only in a major.
func (r *Repo) GetWorkflowDraftByName(ctx context.Context, userID, name string) (*WorkflowDraft, error) {
	svc, err := r.workflowDrafts()
	if err != nil {
		return nil, err
	}
	return svc.GetWorkflowDraftByName(ctx, userID, name)
}

// Deprecated: use workflowdrafts.Service.ListWorkflowDraftsByUser.
func (r *Repo) ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*WorkflowDraft, error) {
	svc, err := r.workflowDrafts()
	if err != nil {
		return nil, err
	}
	return svc.ListWorkflowDraftsByUser(ctx, userID)
}

// Deprecated: use workflowdrafts.Service.UpdateWorkflowDraftDefinition.
func (r *Repo) UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, status WorkflowDraftStatus) error {
	svc, err := r.workflowDrafts()
	if err != nil {
		return err
	}
	return svc.UpdateWorkflowDraftDefinition(ctx, id, name, slug, definition, status)
}

// Deprecated: use workflowdrafts.Service.UpdateWorkflowDraftDefinitionAtVersion.
func (r *Repo) UpdateWorkflowDraftDefinitionAtVersion(ctx context.Context, id string, version int64, name string, slug string, definition string, status WorkflowDraftStatus) error {
	svc, err := r.workflowDrafts()
	if err != nil {
		return err
	}
	return svc.UpdateWorkflowDraftDefinitionAtVersion(ctx, id, version, name, slug, definition, status)
}

// ==================== Preset Repository ====================

func (r *Repo) CreatePreset(ctx context.Context, preset *Preset) (*Preset, error) {
	return store.InsertPreset(ctx, r.ORM(), preset)
}

func (r *Repo) GetPresetBySlug(ctx context.Context, userID, slug string) (*Preset, error) {
	return store.GetPresetBySlug(ctx, r.ORM(), userID, slug)
}

func (r *Repo) GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*Preset, error) {
	return store.GetPresetBySlugAndProject(ctx, r.ORM(), userID, slug, projectID)
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

func (r *Repo) ListWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) ([]*WorkflowScenario, error) {
	return store.ListDraftWorkflowScenarios(ctx, r.ORM(), workflowDraftID)
}

func (r *Repo) UpdateWorkflowScenarioResult(ctx context.Context, id string, status string, result string) error {
	return store.RecordWorkflowScenarioResult(ctx, r.ORM(), id, status, result)
}

func (r *Repo) DeleteWorkflowScenario(ctx context.Context, id string) error {
	return store.RemoveWorkflowScenario(ctx, r.ORM(), id)
}

// ==================== Questions ====================

func (r *Repo) GetQuestionByID(ctx context.Context, id string) (*Question, error) {
	if id == "" {
		return nil, fmt.Errorf("question ID cannot be empty")
	}
	return store.GetQuestion(ctx, r.ORM(), id)
}

// ==================== Helper Functions ====================

// generateID generates a new UUID string
func generateID() string {
	return uuid.New().String()
}
