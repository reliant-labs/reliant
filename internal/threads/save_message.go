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
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// ToolCall is an alias for the canonical message.ToolCall type.
type ToolCall = message.ToolCall

// ToolResult is an alias for the canonical message.ToolResult type.
type ToolResult = message.ToolResult

// ThinkingContent contains extended thinking content from the LLM.
//
// Content and Signature are independently optional. A turn can legitimately
// produce a Signature with no Content — the provider streamed a signed thinking
// block whose text it withheld — and that pairing MUST still be persisted: the
// signature is what lets the next turn replay the block and continue, so
// dropping it strands the thread on a prompt that can only reproduce itself.
type ThinkingContent struct {
	Content   string `json:"content"`
	Signature string `json:"signature"`
	// Redacted is the opaque, encrypted payload of a redacted_thinking block.
	// No readable text; replayed to the provider unchanged.
	Redacted string `json:"redacted,omitempty"`
}

// HasContent reports whether this carries anything worth a durable row —
// readable reasoning, a replayable signature, or a sealed block. Used by the
// write guard so "is there something to save" is defined in exactly one place
// and cannot drift from what createAssistantContentBlocks actually emits.
func (t *ThinkingContent) HasContent() bool {
	if t == nil {
		return false
	}
	return t.Content != "" || t.Signature != "" || t.Redacted != ""
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
	Cost       float64

	// Provenance (provided by caller from LLM resolution / workflow context)
	// Model is the concrete model that served the completion, captured after tag
	// resolution (e.g. "claude-4.8-opus"). Persisted to messages.model.
	Model string
	// Agent is the agent/workflow identity that produced the message
	// (e.g. "builtin://agent", "get-it-right"). Persisted to messages.agent.
	Agent string

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

	// MessageID, when non-empty, is the pre-allocated id to persist the
	// message under (delta identity protocol — streamed deltas were stamped
	// with this id, so the saved row must match). Empty generates a uuid.
	MessageID string
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
			ForkAtMessageID:       nil,         // Compaction is not a branch
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

	// Second idempotency check, keyed on the PRE-ALLOCATED id rather than the
	// activity id. Both are needed, because a turn can reach this function
	// twice under two different activity ids:
	//
	//   1. CallLLM persists an interrupted turn's partial itself, keyed on the
	//      stable callLLMIdempotencyKey (workflow, step, loop iteration).
	//   2. The workflow re-dispatches the save_message node for that same turn,
	//      whose RuntimeContext carries no MessageIdempotencyKey, so it falls
	//      back to a Temporal-minted <workflow>-<run>-<activity> key.
	//
	// The keys differ, so the activity-id check above sees nothing and decides
	// this is a fresh write — but both writes carry the SAME pre-allocated
	// MessageID, so the insert collides on messages_pkey. The collision is
	// deterministic, so every retry fails identically: five failed attempts, an
	// error banner per attempt, and a thread that cannot advance because the
	// surviving partial is now its tail.
	//
	// The pre-allocated id is the real uniqueness constraint here, so it is
	// what convergence has to key on. Reconciling on it also gets the delta
	// identity protocol right by construction: streamed deltas were stamped
	// with this id, so the one persisted row must be the one they name.
	if opts.MessageID != "" {
		existing, err := s.checkExistingMessageByID(ctx, opts, cw)
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

	timestamp := now()
	messageID := uuid.New().String()
	if opts.MessageID != "" {
		messageID = opts.MessageID
	}

	// Build everything the write needs BEFORE opening the transaction.
	//
	// Block construction is pure except for attachment lookup, and doing it
	// out here means the durable write below is a single round trip with no
	// reads holding locks open. This is the whole point of the rewrite: the
	// previous version made 5+N round trips inside a SERIALIZABLE transaction
	// (MAX(ordinal) scan, a WITH RECURSIVE seq walk, the message insert, one
	// insert per content block, then the chat_update), and the duration of
	// that transaction — not the amount of work in it — was what produced the
	// 40001 storms.
	blocks, err := s.buildContentBlocks(ctx, messageID, opts, timestamp)
	if err != nil {
		return nil, err
	}

	write := db.AtomicMessageWrite{
		MessageID:       messageID,
		ChatID:          opts.ChatID,
		ThreadID:        opts.Thread,
		ContextWindowID: cw.ID,
		Role:            reliantv1.MessageRole(opts.Role),
		DisplayStyle:    displayStylePtrIfNonZero(opts.DisplayStyle),
		Model:           ptr.StringIfNotEmpty(opts.Model),
		Agent:           ptr.StringIfNotEmpty(opts.Agent),
		TokenCount:      ptr.IntIfPositive(opts.TokenCount),
		Cost:            ptr.Float64IfPositive(opts.Cost),
		WorkflowID:      opts.WorkflowID,
		NodeID:          ptr.StringIfNotEmpty(opts.StepID),
		ActivityID:      opts.ActivityID,
		CreatedAt:       timestamp,
		Blocks:          blocks,

		ChatUpdateType:   db.UpdateTypeMessage,
		ChatUpdateEntity: messageID,
		// ordinal and seq are not known until the statement allocates them,
		// so the payload is rendered from the RETURNING values.
		ChatUpdateData: func(ordinal, seq, _ int64) (string, error) {
			return s.buildChatUpdateData(ctx, opts, messageID, blocks, ordinal, seq, cw.Sequence, threadTokenCount, timestamp)
		},
	}

	written, err := s.repo.SaveMessageAtomic(ctx, write)
	if err != nil {
		return nil, fmt.Errorf("failed to save message: %w", err)
	}

	result := SaveMessageResult{
		MessageID:        messageID,
		Ordinal:          written.Ordinal,
		ContextWindowID:  cw.ID,
		ThreadTokenCount: threadTokenCount,
		MessageCount:     messageCount + 1,
		ToolCalls:        opts.ToolCalls,
		ToolResults:      opts.ToolResults,
		WasExisting:      false,
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
		// An assistant message with nothing in it produces a row with ZERO
		// content blocks, and that row is durable poison: every later turn
		// loads it as the tail of the conversation, so CallLLM's
		// end-of-history guard fires with
		//
		//   conversation history ends with assistant message after all
		//   transformations ... this usually means the user message was saved
		//   to a different thread than CallLLM reads from
		//
		// which is a misleading message for this cause — nothing is misrouted.
		// The chat then cannot advance: the retry ladder re-runs CallLLM, the
		// same empty row is still the tail, and it fails identically. Measured
		// on real data: 22 such rows accumulated, and the two newest (both at
		// 18:10:14) are exactly the two chats that wedged, at 24 logged
		// failures each.
		//
		// This used to warn and continue. Warning was the wrong call: the row
		// it allows is unrecoverable without hand-editing the database,
		// whereas rejecting the write costs only the turn that produced
		// nothing — and a turn that produced nothing has no content to lose.
		// Fail closed, at the point where the bad state would be created.
		//
		// Thinking counts as content: createAssistantContentBlocks emits a
		// thinking block, so a thinking-only turn is NOT blockless and stays
		// allowed. The condition below mirrors that function's inputs exactly,
		// so this predicate and the block-creation logic cannot drift.
		//
		// "Thinking" here includes a signature or a sealed redacted payload
		// with no readable text. That pairing used to be rejected as blockless,
		// which was the bug: the provider had signed a real thinking block, and
		// refusing the row threw away the one artifact that lets the next turn
		// resume — so the retry replayed an identical prompt and stalled the
		// same way, indefinitely.
		if opts.Content == "" && len(opts.ToolCalls) == 0 && !opts.Thinking.HasContent() {
			return fmt.Errorf(
				"refusing to save an assistant message with no content, tool calls, or thinking "+
					"(chat=%s, thread=%s): the row would have zero content blocks and would wedge "+
					"every subsequent turn of this thread",
				opts.ChatID, opts.Thread,
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

// checkExistingMessageByID reconciles against a row already written under the
// caller's pre-allocated message id, whatever activity wrote it.
//
// Convergence, not replacement: the existing row wins and is returned as-is.
// The alternative — delete and re-insert with this call's content — is wrong
// here in a way that matters. The two writers are not retries of one write;
// they are two views of the same turn, and the FIRST one is the one that
// observed the stream. CallLLM persists exactly what it received before
// unwinding, whereas the re-dispatched save_message node runs after the turn
// yielded and can carry less (an interrupted turn's node re-evaluation has no
// stream to read). Deleting the richer row to write the poorer one loses the
// partial the interrupt was trying to save. It would also cascade the existing
// row's content blocks and tool_calls away, and any agent_messages row
// pointing at it via delivered_message_id would have that reference nulled.
//
// The retry ladder is unaffected: a genuine Temporal retry of ONE writer still
// matches on activity id in checkExistingMessage above, which keeps its
// delete-and-recreate behavior for a half-written attempt.
func (s *Service) checkExistingMessageByID(ctx context.Context, opts SaveMessageOpts, cw *db.ContextWindow) (*SaveMessageResult, error) {
	existing, err := s.repo.FindMessage(ctx, opts.MessageID)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing message by id: %w", err)
	}
	if existing == nil {
		return nil, nil
	}

	slog.Info("SaveMessage: Converging on the row already written under this pre-allocated id",
		"messageID", existing.ID,
		"existingActivityID", ptr.From(existing.ActivityID),
		"incomingActivityID", ptr.From(opts.ActivityID),
		"attemptNumber", opts.AttemptNumber)

	messageCount, err := s.repo.GetEffectiveMessageCount(ctx, opts.ChatID, opts.Thread)
	if err != nil {
		return nil, fmt.Errorf("failed to get effective message count: %w", err)
	}

	return &SaveMessageResult{
		MessageID:        existing.ID,
		Ordinal:          existing.Ordinal,
		ContextWindowID:  cw.ID,
		ThreadTokenCount: opts.TokenCount,
		MessageCount:     messageCount,
		ToolCalls:        opts.ToolCalls,
		ToolResults:      opts.ToolResults,
		WasExisting:      true,
	}, nil
}

// createContentBlocks creates the appropriate content blocks based on message role.
func (s *Service) buildContentBlocks(ctx context.Context, messageID string, opts SaveMessageOpts, timestamp time.Time) ([]db.MessageContentBlock, error) {
	switch reliantv1.MessageRole(opts.Role) {
	case reliantv1.MessageRole_MESSAGE_ROLE_USER:
		return s.buildUserContentBlocks(ctx, messageID, opts, timestamp)
	case reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT:
		return s.buildAssistantContentBlocks(messageID, opts, timestamp), nil
	case reliantv1.MessageRole_MESSAGE_ROLE_TOOL:
		return s.buildToolContentBlocks(ctx, messageID, opts, timestamp)
	case reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM:
		return s.buildSystemContentBlocks(messageID, opts, timestamp), nil
	}
	return nil, nil
}

// createUserContentBlocks creates content blocks for user messages.
// buildUserContentBlocks is one of two builders that take a context (the other
// is buildToolContentBlocks): both resolve attachment metadata, and that read
// must happen BEFORE the write statement rather than inside it. Reading here
// keeps the durable write a single round trip with no lookups holding locks
// open.
func (s *Service) buildUserContentBlocks(ctx context.Context, messageID string, opts SaveMessageOpts, timestamp time.Time) ([]db.MessageContentBlock, error) {
	var blocks []db.MessageContentBlock
	position := 0

	// Create text block if content is provided
	if opts.Content != "" {
		blocks = append(blocks, db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: messageID,
			Position:  position,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &opts.Content,
			Version:   ptr.Of(1),
			CreatedAt: timestamp,
			UpdatedAt: timestamp,
		})
		position++
	}

	// Create attachment blocks
	for _, attachmentID := range opts.Attachments {
		att, err := s.repo.GetAttachment(ctx, attachmentID)
		if err != nil {
			return nil, fmt.Errorf("failed to get attachment metadata for %s: %w", attachmentID, err)
		}
		if att == nil {
			return nil, fmt.Errorf("failed to get attachment metadata for %s: attachment not found", attachmentID)
		}

		var blockType reliantv1.ContentBlockType
		switch att.AttachmentType {
		case string(attachment.TypeImage):
			blockType = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE
		case string(attachment.TypeFileReference):
			blockType = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE
		case "document":
			blockType = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_DOCUMENT
		default:
			return nil, fmt.Errorf("invalid attachment type %q for attachment %s", att.AttachmentType, attachmentID)
		}

		attachmentRef := attachmentID
		blocks = append(blocks, db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: messageID,
			Position:  position,
			BlockType: blockType,
			Content:   &attachmentRef,
			Version:   ptr.Of(1),
			CreatedAt: timestamp,
			UpdatedAt: timestamp,
		})
		position++
	}

	return blocks, nil
}

// createAssistantContentBlocks creates content blocks for assistant messages.
func (s *Service) buildAssistantContentBlocks(messageID string, opts SaveMessageOpts, timestamp time.Time) []db.MessageContentBlock {
	var blocks []db.MessageContentBlock
	position := 0

	// Create thinking block when there is readable reasoning OR a signature.
	//
	// The signature alone is worth a row: it is what the provider verifies to
	// let the model resume its own reasoning next turn. Persisting text-only
	// used to drop signature-bearing turns entirely, which is what wedged a
	// thread into replaying the same prompt forever.
	if opts.Thinking != nil && (opts.Thinking.Content != "" || opts.Thinking.Signature != "") {
		var thinkingSig *string
		if opts.Thinking.Signature != "" {
			thinkingSig = &opts.Thinking.Signature
		}

		slog.Info("[SaveMessage] Creating thinking block",
			"message_id", messageID,
			"thinking_len", len(opts.Thinking.Content),
			"has_signature", opts.Thinking.Signature != "",
			"position", position)

		blocks = append(blocks, db.MessageContentBlock{
			ID:               uuid.New().String(),
			MessageID:        messageID,
			Position:         position,
			BlockType:        reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING,
			Content:          &opts.Thinking.Content,
			ThoughtSignature: thinkingSig,
			Version:          ptr.Of(1),
			CreatedAt:        timestamp,
			UpdatedAt:        timestamp,
		})
		position++
	}

	// Create the redacted thinking block, if the provider sealed one. Its own
	// block type, so nothing downstream can read the ciphertext as reasoning.
	if opts.Thinking != nil && opts.Thinking.Redacted != "" {
		slog.Info("[SaveMessage] Creating redacted thinking block",
			"message_id", messageID,
			"data_len", len(opts.Thinking.Redacted),
			"position", position)

		blocks = append(blocks, db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: messageID,
			Position:  position,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING,
			Content:   &opts.Thinking.Redacted,
			Version:   ptr.Of(1),
			CreatedAt: timestamp,
			UpdatedAt: timestamp,
		})
		position++
	}

	// Create text block if content is provided
	if opts.Content != "" {
		blocks = append(blocks, db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: messageID,
			Position:  position,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &opts.Content,
			Version:   ptr.Of(1),
			CreatedAt: timestamp,
			UpdatedAt: timestamp,
		})
		position++
	}

	// Create tool_call blocks
	for i, toolCall := range opts.ToolCalls {
		var thoughtSig *string
		if toolCall.ThoughtSignature != "" {
			thoughtSig = &toolCall.ThoughtSignature
		}

		blocks = append(blocks, db.MessageContentBlock{
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
		})
	}

	return blocks
}

// buildToolContentBlocks builds the content blocks for tool messages.
//
// A tool result may carry attachment ids (see message.ToolResult.AttachmentIDs).
// Each one becomes a sibling IMAGE block immediately after its tool_result
// block, in exactly the representation the user-upload path produces: block
// type IMAGE with the attachment id as Content. Reusing that representation is
// the whole point — the attachment fetch, the /api/attachments/{id} serving and
// the existing renderer all key off it, so a generated image travels the same
// road as an uploaded one rather than needing a second one built for it.
//
// This BUILDS rather than writes, unlike the createToolContentBlocks it
// replaces: every block for a message now goes in through one atomic statement
// (SaveMessageAtomic) so concurrent writers cannot collide on seq. The
// attachment lookup still needs a DB read, so this keeps a ctx and an error
// return even though nothing here writes.
//
// `position` is therefore explicit and no longer the loop index: an attachment
// block occupies a position of its own, so with any attachment present the
// tool_result for result i is no longer at position i.
func (s *Service) buildToolContentBlocks(ctx context.Context, messageID string, opts SaveMessageOpts, timestamp time.Time) ([]db.MessageContentBlock, error) {
	blocks := make([]db.MessageContentBlock, 0, len(opts.ToolResults))
	position := 0
	for _, result := range opts.ToolResults {
		blocks = append(blocks, db.MessageContentBlock{
			ID:         uuid.New().String(),
			MessageID:  messageID,
			Position:   position,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolCallID: &result.ToolCallID,
			ToolName:   ptr.StringIfNotEmpty(result.Name),
			Content:    &result.Content,
			IsError:    &result.IsError,
			Version:    ptr.Of(1),
			CreatedAt:  timestamp,
			UpdatedAt:  timestamp,
		})
		position++

		for _, attachmentID := range result.AttachmentIDs {
			block, ok := s.buildToolAttachmentBlock(ctx, messageID, attachmentID, position, timestamp)
			if !ok {
				continue
			}
			blocks = append(blocks, block)
			position++
		}
	}
	return blocks, nil
}

// buildToolAttachmentBlock materializes one attachment id as a content block
// on a tool message, reporting whether there is a block to append.
//
// A missing or non-image attachment is skipped rather than failing the save.
// That is the opposite of the user-upload path, and deliberately so: there, the
// ids come from a request the user just made and a bad one is a bug worth
// surfacing, whereas here the message also carries the tool's textual result
// and the model's own view of the image. Failing the whole save would discard a
// completed turn — including work that cost money — over a thumbnail.
//
// It BUILDS rather than writes (it used to end in CreateContentBlock), because
// every block for a message is now inserted by one atomic statement. The skip
// semantics above are what let the return be a plain (block, ok) rather than an
// error: every failure mode here is already "log it and carry on", so there is
// no error left for a caller to handle.
func (s *Service) buildToolAttachmentBlock(ctx context.Context, messageID, attachmentID string, position int, timestamp time.Time) (db.MessageContentBlock, bool) {
	if attachmentID == "" {
		return db.MessageContentBlock{}, false
	}

	att, err := s.repo.GetAttachment(ctx, attachmentID)
	if err != nil {
		slog.Warn("[SaveMessage] Failed to load tool result attachment; skipping its content block",
			"error", err, "attachment_id", attachmentID, "message_id", messageID)
		return db.MessageContentBlock{}, false
	}
	if att == nil {
		slog.Warn("[SaveMessage] Tool result attachment not found; skipping its content block",
			"attachment_id", attachmentID, "message_id", messageID)
		return db.MessageContentBlock{}, false
	}

	if att.AttachmentType != string(attachment.TypeImage) {
		slog.Warn("[SaveMessage] Tool result attachment is not an image; skipping its content block",
			"attachment_id", attachmentID, "attachment_type", att.AttachmentType, "message_id", messageID)
		return db.MessageContentBlock{}, false
	}

	attachmentRef := attachmentID
	return db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: messageID,
		Position:  position,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE,
		Content:   &attachmentRef,
		Version:   ptr.Of(1),
		CreatedAt: timestamp,
		UpdatedAt: timestamp,
	}, true
}

// createSystemContentBlocks creates content blocks for system messages.
func (s *Service) buildSystemContentBlocks(messageID string, opts SaveMessageOpts, timestamp time.Time) []db.MessageContentBlock {
	if opts.Content == "" {
		return nil
	}
	return []db.MessageContentBlock{{
		ID:        uuid.New().String(),
		MessageID: messageID,
		Position:  0,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   &opts.Content,
		Version:   ptr.Of(1),
		CreatedAt: timestamp,
		UpdatedAt: timestamp,
	}}
}

// emitChatUpdate emits a chat_update for the frontend.
// buildChatUpdateData renders the chat_update payload for a message.
//
// It takes the blocks it was ASKED to write rather than re-reading them. The
// previous version issued a ListContentBlocks against rows the same
// transaction had just inserted — a round trip whose only possible answer was
// the slice already in hand, paid while holding locks on the hottest write
// path in the system.
//
// ordinal and seq arrive as parameters because they do not exist until the
// write statement allocates them; the caller supplies them from its RETURNING
// values.
func (s *Service) buildChatUpdateData(ctx context.Context, opts SaveMessageOpts, messageID string, blocks []db.MessageContentBlock, ordinal int64, seq int64, contextSequence int, threadTokenCount int, timestamp time.Time) (string, error) {
	blockPtrs := make([]*db.MessageContentBlock, len(blocks))
	for i := range blocks {
		blockPtrs[i] = &blocks[i]
	}

	// One serializer for a block's wire shape, shared with every other path
	// that emits a live message update (db.ContentBlockPayloads). This is the
	// PRIMARY live path: when it open-coded its own copy of the loop, a spawn's
	// tool-call block shipped without the child_workflow_id that names the
	// thread it owns, and the spawn's preview had nothing to render for the
	// whole run.
	toolCallsByID := make(map[string]*db.ToolCall)
	if calls, err := s.repo.ListToolCallsByMessageIDs(ctx, []string{messageID}); err != nil {
		slog.Warn("failed to load tool calls for chat_update", "error", err, "message_id", messageID)
	} else {
		for _, call := range calls {
			toolCallsByID[call.ID] = call
		}
	}
	contentBlocks := db.ContentBlockPayloadsWithToolCalls(blockPtrs, toolCallsByID)
	attachmentIDs := db.AttachmentIDsFromBlocks(blockPtrs)

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
	compactionThreshold := DefaultCompactionThreshold
	// seq is the chat-global order the client sorts by. This is the primary
	// live-message path, so omitting it means every message a user sends or
	// receives arrives with seq 0 and sorts to the TOP of the transcript
	// instead of the bottom — delivered, but rendered where nobody looks.
	updateData := db.MessageUpdateData{
		UpdateType:          "message",
		ID:                  messageID,
		Role:                opts.Role,
		Seq:                 seq,
		Ordinal:             ordinal,
		Thread:              opts.Thread,
		ContextSequence:     &contextSequence,
		CreatedAt:           timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"),
		UpdatedAt:           timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"),
		ContentBlocks:       contentBlocks,
		Attachments:         &attachments,
		ThreadTokenCount:    &threadTokenCount,
		CompactionThreshold: &compactionThreshold,
	}

	// Add optional fields
	if opts.DisplayStyle != 0 {
		updateData.DisplayStyle = &opts.DisplayStyle
	}
	if opts.TokenCount > 0 {
		updateData.TokenCount = &opts.TokenCount
	}

	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		slog.Error("[SaveMessage] Failed to marshal chat_update data",
			"error", err,
			"chat_id", opts.ChatID,
			"message_id", messageID)
		return "", fmt.Errorf("failed to marshal chat_update data: %w", err)
	}

	return string(updateDataJSON), nil
}

func displayStylePtrIfNonZero(i int32) *reliantv1.DisplayStyle {
	if i == 0 {
		return nil
	}
	value := reliantv1.DisplayStyle(i)
	return &value
}
