// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/messageconv"
)

// ============================================================================
// SHARED HELPER FUNCTIONS
// ============================================================================

// InterruptedToolResultContent is the stub tool_result content synthesized for
// dangling tool calls (assistant tool_use with no persisted result), e.g. when
// a run was killed mid-execution and a later run resumes on the same thread.
// The wording matters: the tool may or may not have taken effect, so the model
// must verify before re-running side-effectful calls.
const InterruptedToolResultContent = "Tool execution was interrupted — outcome unknown. The previous run was interrupted before the result was recorded; verify the effects of this call before re-running it."

// LoadMessagesForLLM loads messages for LLM context with full repair support.
// This is the primary function for loading messages to send to the LLM.
//
// LAYERED REPAIR STRATEGY for orphaned tool calls:
// Orphaned tool_calls (tool_call blocks without matching tool_result) can cause LLM API errors.
// We use a defense-in-depth approach with multiple repair layers:
//
//	Layer 1 (Primary): CleanupActivity persists repairs when workflows end
//	Layer 2 (Defense): This function's DB repair catches missed orphans (e.g., cleanup didn't run)
//	Layer 3 (Fallback): In-memory repair via repairMessageHistory() handles inherited messages
//	                    and edge cases where DB repair isn't possible
//
// This ensures conversations can always recover to a valid state for LLM calls.
//
// It handles:
//   - Automatic context window discovery (uses latest context window)
//   - Fork chain traversal (inherits messages from parent threads)
//   - Compaction boundary detection (stops at CompactionSummaryMessageID, NOT Sequence > 0)
//   - DB-level orphan repair (Layer 2) - persists repairs for subsequent calls
//   - In-memory orphan repair (Layer 3) - handles inherited and edge cases
//
// Parameters:
//   - chatID: The chat to load messages from
//   - thread: The thread ID to load messages for
//   - explicitContextSeq: Optional explicit context sequence (nil = auto-detect)
//
// Returns:
//   - messages: Converted messages in chronological order, ready for LLM
//   - error: Any error that occurred
func LoadMessagesForLLM(ctx context.Context, repo db.Repository, chatID string, thread string, explicitContextSeq *int) ([]message.Message, error) {
	if thread == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	// Use threads.Service.LoadCurrentMessages for proper fork chain and compaction handling.
	// This fixes the bug where forked threads inherit sequence numbers but don't have
	// compaction summaries, causing incorrect parent traversal skipping.
	svc := threads.NewService(repo)
	dbMessages, err := svc.LoadCurrentMessages(ctx, thread)
	if err != nil {
		return nil, fmt.Errorf("failed to load current messages: %w", err)
	}

	// Layer 2: Attempt DB-level repair for orphaned tool_calls.
	// This persists repairs so subsequent calls don't need to redo them.
	// Primary repair should have been done by CleanupActivity (Layer 1),
	// but this catches cases where cleanup didn't run or failed.

	// Get context sequence for repair operations
	contextSequence := 0
	if explicitContextSeq != nil {
		contextSequence = *explicitContextSeq
	} else {
		maxSeq, err := repo.GetMaxContextSequenceInThread(ctx, thread)
		if err == nil && maxSeq > 0 {
			contextSequence = maxSeq
		}
	}

	// Attempt to repair orphaned tool_calls by persisting synthetic tool_results to DB.
	repaired, err := RepairOrphanedToolCalls(ctx, repo, chatID, thread, contextSequence, dbMessages)
	if err != nil {
		logging.Warn("[LoadMessagesForLLM] Failed to repair orphaned tool calls",
			"error", err,
			"chatID", chatID)
		// Continue - ConvertAndRepairMessages will do in-memory repair as fallback
	}

	// If DB repairs were made, reload messages to get fresh state.
	// Otherwise, convert what we have (ConvertAndRepairMessages handles in-memory repair).
	if repaired {
		dbMessages, err = svc.LoadCurrentMessages(ctx, thread)
		if err != nil {
			return nil, fmt.Errorf("failed to reload messages after repair: %w", err)
		}
	}

	// Convert and do in-memory repair (Layer 3)
	return ConvertAndRepairMessages(ctx, dbMessages, repo)
}

// RepairOrphanedToolCalls checks for assistant messages with tool_calls that don't have
// matching tool_results, and persists synthetic tool_result messages to the database.
// Returns true if any repairs were made.
//
// This is Layer 2 of the repair strategy - it runs before LLM calls to ensure
// any missed orphans from Layer 1 (CleanupActivity) are repaired.
//
// NOTE: Only repairs orphans in the LAST assistant message. Mid-conversation orphans
// are handled by the in-memory repair (Layer 3) since ordinal insertion is complex.
//
// Orphaned tool calls can occur when:
// 1. A workflow is cancelled mid-execution (after tool_call saved but before tool_result)
// 2. A crash occurs during tool execution
// 3. User interrupts with a new message before tool results arrive
// 4. CleanupActivity failed or didn't run
func RepairOrphanedToolCalls(ctx context.Context, repo db.Repository, chatID string, thread string, contextSequence int, dbMessages []*db.Message) (bool, error) {
	if len(dbMessages) == 0 {
		return false, nil
	}

	// Build a set of tool_call IDs that have results
	toolResultIDs := make(map[string]bool)
	for _, dbMsg := range dbMessages {
		if dbMsg.Role == reliantv1.MessageRole_MESSAGE_ROLE_TOOL {
			blocks, err := repo.ListContentBlocks(ctx, dbMsg.ID)
			if err != nil {
				continue
			}
			for _, block := range blocks {
				if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT && block.ToolCallID != nil {
					toolResultIDs[*block.ToolCallID] = true
				}
			}
		}
	}

	// Find orphaned tool_calls in the LAST assistant message only.
	// Mid-conversation orphans are handled by in-memory repair since ordinal insertion is complex.
	// This handles the common case: workflow cancelled at the end before tool results saved.
	var lastAssistant *db.Message
	for i := len(dbMessages) - 1; i >= 0; i-- {
		if dbMessages[i].Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			lastAssistant = dbMessages[i]
			break
		}
	}

	if lastAssistant == nil {
		return false, nil
	}

	blocks, err := repo.ListContentBlocks(ctx, lastAssistant.ID)
	if err != nil {
		return false, fmt.Errorf("failed to list content blocks: %w", err)
	}

	var orphanedToolCalls []*db.MessageContentBlock
	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && block.ToolCallID != nil {
			if !toolResultIDs[*block.ToolCallID] {
				orphanedToolCalls = append(orphanedToolCalls, block)
			}
		}
	}

	if len(orphanedToolCalls) == 0 {
		return false, nil
	}

	logging.Info("[RepairOrphanedToolCalls] Repairing orphaned tool calls from last assistant message",
		"chatID", chatID,
		"assistantMessageID", lastAssistant.ID,
		"orphanedCount", len(orphanedToolCalls))

	// Create synthetic tool message at the next ordinal (end of conversation)
	msgID := fmt.Sprintf("repair-%s-%d", lastAssistant.ID, time.Now().UnixNano())
	// UTC to match every other persisted timestamp — a local-time value here
	// puts repair messages hours in the past and breaks time-ordering.
	now := time.Now().UTC()

	nextOrdinal, err := repo.GetNextOrdinal(ctx, thread)
	if err != nil {
		return false, fmt.Errorf("failed to get next ordinal: %w", err)
	}

	// Derive context_window_id from thread and contextSequence
	contextWindowID := fmt.Sprintf("%s:%s:%d", chatID, thread, contextSequence)

	syntheticMsg := &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		Ordinal:         nextOrdinal,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := repo.CreateMessage(ctx, syntheticMsg); err != nil {
		return false, fmt.Errorf("failed to create repair message: %w", err)
	}

	// Create content blocks for each orphaned tool call
	for i, tc := range orphanedToolCalls {
		blockID := fmt.Sprintf("%s-block-%d", msgID, i)
		isError := true
		content := InterruptedToolResultContent

		block := &db.MessageContentBlock{
			ID:         blockID,
			MessageID:  msgID,
			Position:   i,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			Content:    &content,
			ToolName:   tc.ToolName,
			ToolCallID: tc.ToolCallID,
			IsError:    &isError,
			IsComplete: true,
			CreatedAt:  now,
			UpdatedAt:  now,
		}

		if err := repo.CreateContentBlock(ctx, block); err != nil {
			logging.Error("[RepairOrphanedToolCalls] Failed to create repair content block",
				"error", err,
				"chatID", chatID,
				"blockID", blockID,
				"toolCallID", *tc.ToolCallID)
			// Continue with other blocks
			continue
		}

		logging.Info("[RepairOrphanedToolCalls] Created repair tool_result",
			"chatID", chatID,
			"messageID", msgID,
			"toolCallID", *tc.ToolCallID,
			"toolName", tc.ToolName)
	}

	return true, nil
}

// ConvertAndRepairMessages converts DB messages to message.Message and repairs
// any orphaned tool_calls (adds synthetic tool_results). This ensures the message
// thread is valid for the Anthropic API which requires every tool_use to have
// a corresponding tool_result immediately after.
//
// Use this function when you already have DB messages loaded.
func ConvertAndRepairMessages(ctx context.Context, dbMessages []*db.Message, repo db.Repository) ([]message.Message, error) {
	msgs := make([]message.Message, 0, len(dbMessages))
	for _, dbMsg := range dbMessages {
		msg, err := messageconv.DbMessageToMessage(ctx, dbMsg, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to convert message %s: %w", dbMsg.ID, err)
		}
		msgs = append(msgs, msg)
	}

	// Repair message history to ensure tool_calls have matching tool_results.
	// This handles cases where a workflow was cancelled mid-execution or crashed.
	msgs = repairMessageHistory(msgs)

	return msgs, nil
}
