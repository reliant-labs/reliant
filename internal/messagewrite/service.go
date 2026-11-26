// yours: scaffolded once, never touched again — forge will not overwrite this file
// Service implementation; canonical pattern in `forge skill load service-layer`.
package messagewrite

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Deps contains the transaction and generated-store roles used by the writer.
type Deps struct {
	Tx          Tx
	Order       Order
	Messages    Messages
	Windows     Windows
	Threads     Threads
	Workflows   Workflows
	Blocks      Blocks
	Attachments Attachments
	Updates     Updates
}

// ─── The contract ───────────────────────────────────────────────────
//
// The //forge:contract marker on `service` below makes its exported
// methods the messagewrite package boundary. `forge generate` derives
// `type Service interface` from them into contract_gen.go (forge-owned:
// never edit it), and mock_gen.go and middleware_gen.go from that — so a
// method is declared exactly once, here, on *service.
//
//   - Every exported method declared on service IS a Service method; keep
//     helpers unexported. Exported types, functions (non-methods),
//     constants, and variables are unrestricted.
//   - Give each method a doc comment: it is copied onto the interface
//     method in contract_gen.go, which is what callers read.
//   - Re-run `forge generate` after adding, removing or re-signing a
//     method. Until it runs, callers holding a Service cannot see a new
//     method, and a removed one fails contract_gen.go's
//     `var _ Service = (*service)(nil)` check.
//
// A method looks like this:
//
//	// Get returns the thing stored under id.
//	func (s *service) Get(ctx context.Context, id string) (Thing, error) {
//		return s.deps.Store.Get(ctx, id)
//	}

//forge:contract
type service struct {
	deps Deps
}

// New creates a new messagewrite service instance.
// Returns the Service interface — callers interact only through the contract.
//
// The two-result `(Service, error)` shape lets New surface dep-validation
// or one-time-init failures (missing required deps, bad config, etc.).
// Scaffolds ship with a no-error path; polish the body when you grow
// dep validation or eager initialization.
//
// forge:constructor
//
// The marker above opts this package into the generated observability
// decorator (span / metrics / structured logging / panic-recovery on every
// Service method, routed through the owned observe_chain.go seam). Remove it
// and add `// forge:no-observe` on this line to opt the package out; add
// `// forge:no-observe` to a single method's doc comment to skip just that
// method.
func New(deps Deps) (Service, error) {
	if deps.Tx == nil || deps.Order == nil || deps.Messages == nil || deps.Windows == nil || deps.Threads == nil || deps.Workflows == nil || deps.Blocks == nil || deps.Attachments == nil || deps.Updates == nil {
		return nil, fmt.Errorf("messagewrite: all dependencies are required")
	}
	return &service{deps: deps}, nil
}

// SaveMessageToThread atomically saves a message, blocks and its chat update.
func (s *service) SaveMessageToThread(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32) (*core.Message, error) {
	return s.SaveMessageToThreadWithID(ctx, chatID, thread, role, content, workflowID, attachmentIDs, displayStyle, "")
}

// SaveMessageToThreadWithID saves a message with an optional caller-chosen ID.
func (s *service) SaveMessageToThreadWithID(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string) (*core.Message, error) {
	msg, _, err := s.saveMessageToThread(ctx, chatID, thread, role, content, workflowID, attachmentIDs, displayStyle, messageID, false)
	return msg, err
}

// SaveMessageToThreadOnce saves at most once under a caller-chosen ID.
func (s *service) SaveMessageToThreadOnce(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string) (*core.Message, bool, error) {
	if messageID == "" {
		return nil, false, fmt.Errorf("messageID cannot be empty")
	}
	return s.saveMessageToThread(ctx, chatID, thread, role, content, workflowID, attachmentIDs, displayStyle, messageID, true)
}
func displayStyleToProto(value *int32) *reliantv1.DisplayStyle {
	if value == nil {
		return nil
	}
	v := reliantv1.DisplayStyle(*value)
	return &v
}
func (s *service) senderTitle(ctx context.Context, threadID string) *string {
	titles, err := s.deps.Threads.SenderTitlesByThreadIDs(ctx, []string{threadID})
	if err != nil {
		return nil
	}
	title, ok := titles[threadID]
	if !ok {
		return nil
	}
	return &title
}
func (s *service) saveMessageToThread(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string, once bool) (*core.Message, bool, error) {
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
		displayStyle = core.QueuedDisplayStyle(ctx)
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
		if wf, err := s.deps.Workflows.FindWorkflowByThread(ctx, chatID, thread); err == nil && wf != nil && wf.ID != "" {
			owning := wf.ID
			workflowID = &owning
		}
	}

	var savedMsg *core.Message
	created := true
	err := s.deps.Tx.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		// The transaction is retried on a serialization conflict, and a retry
		// starts over.
		savedMsg, created = nil, true

		// The thread's ordinal and the chat's seq come from
		// message_order_counters, never from the highest number already
		// written (store.AllocateMessageOrder). When an id already taken makes
		// a once-save write nothing, the numbers it took are a gap.
		ordinal, seq, err := s.deps.Order.AllocateMessageOrder(txCtx, thread, chatID)
		if err != nil {
			return err
		}

		// Get the current context_sequence for the thread
		// This ensures user messages are saved at the same context_sequence as other messages
		// (important after compaction when context_sequence > 0)
		contextSequence, err := s.maxContextSequence(txCtx, thread)
		if err != nil {
			return fmt.Errorf("failed to get context sequence: %w", err)
		}

		// Get or create context window for this thread at this sequence
		contextWindowID := fmt.Sprintf("%s:%s:%d", chatID, thread, contextSequence)
		cw, err := s.deps.Windows.GetContextWindow(txCtx, contextWindowID)
		if err != nil || cw == nil {
			// Create new context window
			now := time.Now().UTC()
			newCW := &core.ContextWindow{
				ID:        contextWindowID,
				ThreadID:  thread,
				Sequence:  contextSequence,
				CreatedAt: now,
			}
			if _, err := s.deps.Windows.InsertContextWindow(txCtx, newCW); err != nil {
				return fmt.Errorf("failed to create context window: %w", err)
			}
		}

		// Create the message
		now := time.Now().UTC()
		msgID := uuid.New().String()
		if messageID != "" {
			msgID = messageID
		}
		msg := &core.Message{
			ID:              msgID,
			ChatID:          chatID,
			Ordinal:         ordinal,
			Seq:             seq,
			ThreadID:        thread,
			ContextWindowID: contextWindowID,
			Role:            reliantv1.MessageRole(role),
			DisplayStyle:    displayStyleToProto(displayStyle),
			WorkflowID:      workflowID,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		// A user-role message an agent sent through the human's send path
		// (send_to_run) names its sender, so it is never shown as the human's.
		origin, attributed := core.MessageOriginFrom(ctx)
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
			ok, err := s.deps.Messages.InsertMessageOnce(txCtx, msg)
			if err != nil {
				return fmt.Errorf("failed to create message: %w", err)
			}
			if !ok {
				created = false
				return nil
			}
		} else if err := s.deps.Messages.InsertMessage(txCtx, msg); err != nil {
			return fmt.Errorf("failed to create message: %w", err)
		}

		position := 0

		// Create text content block if there's content
		if content != "" {
			block := &core.MessageContentBlock{
				ID:        uuid.New().String(),
				MessageID: msgID,
				Position:  position,
				BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
				Content:   &content,
			}

			if err := s.deps.Blocks.InsertContentBlock(txCtx, block); err != nil {
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
			att, err := s.deps.Attachments.GetAttachment(txCtx, attachmentID)
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
			block := &core.MessageContentBlock{
				ID:        uuid.New().String(),
				MessageID: msgID,
				Position:  position,
				BlockType: blockType,
				Content:   &attID,
			}

			if err := s.deps.Blocks.InsertContentBlock(txCtx, block); err != nil {
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
			attachmentsData, err := s.deps.Attachments.GetAttachmentsByIDs(txCtx, attachmentIDsForMetadata)
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
		updateData := core.MessageUpdateData{
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
			updateData.Sender = core.NewMessageSenderPayload(origin.ThreadID, s.senderTitle(txCtx, origin.ThreadID), origin.Kind, origin.Subject)
		}

		updateDataJSON, err := updateData.Marshal()
		if err != nil {
			return fmt.Errorf("failed to marshal chat_update data: %w", err)
		}

		if err := s.deps.Updates.CreateChatUpdate(txCtx, chatID, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, msgID, updateDataJSON); err != nil {
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

func (s *service) maxContextSequence(ctx context.Context, threadID string) (int, error) {
	if threadID == "" {
		return 0, fmt.Errorf("thread ID cannot be empty")
	}

	maxSeq, err := s.deps.Windows.GetMaxSequenceForThread(ctx, threadID)
	if err != nil {
		return 0, fmt.Errorf("failed to get max context sequence: %w", err)
	}

	// If we have local context_windows, use the max sequence
	if maxSeq > 0 {
		return int(maxSeq), nil
	}

	// No local context_windows yet - check if this is a forked thread via Thread record
	// If so, inherit the parent's context_sequence at the fork point
	threadRecord, _, err := s.deps.Threads.GetThreadWithParent(ctx, threadID)
	if err != nil {
		logging.DebugContext(ctx, "[GetMaxContextSequenceInThread] Error getting thread, defaulting to 0",
			"threadID", threadID,
			"error", err)
		return 0, nil
	}

	if threadRecord != nil && threadRecord.ParentThreadID != nil && threadRecord.ForkAtMessageID != nil {
		// Get the parent's context_window sequence at the fork point, via the
		// fork message's own context window.
		forkMsg, err := s.deps.Messages.FindMessage(ctx, *threadRecord.ForkAtMessageID)
		if err == nil && forkMsg != nil {
			parentCW, err := s.deps.Windows.GetContextWindow(ctx, forkMsg.ContextWindowID)
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
