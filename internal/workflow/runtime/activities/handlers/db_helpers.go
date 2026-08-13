// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

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

// LoadMessagesForLLM loads messages for LLM context, guaranteeing the result
// satisfies the tool-pairing invariant. This is the primary function for loading
// messages to send to the LLM.
//
// # HOW THE TOOL-PAIRING INVARIANT IS ENFORCED
//
// The hard requirement is that we never hand a provider an assistant turn whose
// tool_use blocks lack matching tool_result blocks — that wedges the conversation
// permanently (see tool_pairing.go). Enforcement is two mechanisms, not a stack
// of overlapping repairs:
//
//	AT REST — the schema makes the bad state unrepresentable.
//	  tool_call_results.tool_call_id is PRIMARY KEY + FOREIGN KEY into
//	  tool_calls(id), so a duplicate result or a result for a nonexistent call
//	  cannot be stored (20260801010000_add_tool_calls.sql). CleanupActivity
//	  additionally records a terminal result when a workflow ends with a call
//	  still open, so the common interrupted-run case is fixed in the database
//	  rather than re-derived on every read.
//
//	AT THE BOUNDARY — ConvertAndRepairMessages closes gaps we cannot fix at rest.
//	  Some orphans are genuinely not ours to repair; see the "legitimate orphan
//	  sources" note there. That pass repairs them in memory and then ASSERTS the
//	  result is valid, so a violation is impossible to ship silently.
//
// A previous third layer persisted synthetic "interrupted" results here, on read,
// for the last assistant message. It was removed: writing a terminal result at
// read time destroys the real outcome of calls that can still legitimately land
// (a backgrounded tool, an in-flight call on a run that is resuming), and the
// boundary repair already produces the same LLM-visible history without mutating
// the conversation. A read path should not have side effects on the record.
//
// It handles:
//   - Automatic context window discovery (uses latest context window)
//   - Fork chain traversal (inherits messages from parent threads)
//   - Compaction boundary detection (stops at CompactionSummaryMessageID, NOT Sequence > 0)
//   - Tool-pairing repair and assertion (see ConvertAndRepairMessages)
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

	return convertAndRepairMessages(ctx, dbMessages, repo, chatID, thread)
}

// ConvertAndRepairMessages converts DB messages to message.Message and
// guarantees the tool-pairing invariant holds on the result.
//
// Use this function when you already have DB messages loaded.
func ConvertAndRepairMessages(ctx context.Context, dbMessages []*db.Message, repo db.Repository) ([]message.Message, error) {
	return convertAndRepairMessages(ctx, dbMessages, repo, "", "")
}

// convertAndRepairMessages is the prompt-assembly boundary: the last point where
// we can still see a broken history before it becomes a provider request.
//
// It repairs, then ASSERTS. The two halves have different failure policies, and
// the split is deliberate:
//
// REPAIR-AND-WARN for violations found in the loaded history. There are orphan
// sources that are legitimately not ours to fix at rest:
//
//   - Fork / branch. A branched chat inherits its parent's messages through the
//     context-window chain, and the fork ordinal can fall between an assistant
//     message and the tool message answering it. The inherited rows belong to
//     the PARENT conversation; writing repair rows into them would corrupt a
//     conversation the user did not touch, and the child cannot rewrite history
//     it only borrows.
//   - A crash between persisting the assistant message and persisting its
//     results, on a chat that is then read before CleanupActivity runs (or where
//     cleanup never ran because the process died).
//
// Failing the call here would turn a recoverable history into exactly the
// permanent deadlock this invariant exists to prevent — the user could never
// send another message in that chat. So we synthesize the missing results, log
// loudly with the ids, and let the conversation continue.
//
// ASSERT on anything still broken afterwards. Post-repair violations are not
// inherited data, they are a bug in this package. Returning an error surfaces it
// at the activity boundary (retried and reported) instead of shipping a request
// the provider will reject with a far less actionable message.
func convertAndRepairMessages(ctx context.Context, dbMessages []*db.Message, repo db.Repository, chatID, thread string) ([]message.Message, error) {
	msgs, err := messageconv.DbMessagesToMessages(ctx, dbMessages, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to convert messages: %w", err)
	}

	if violations := ValidateToolPairing(msgs); len(violations) > 0 {
		logging.Warn("[ToolPairing] Loaded history violates the tool-call/tool-result invariant; repairing in memory",
			"chatID", chatID,
			"thread", thread,
			"violationCount", len(violations),
			"violations", summarizeViolations(violations),
			"messageCount", len(msgs))
	}

	msgs = repairMessageHistory(msgs)

	// The invariant must hold from here on. A violation now means repair itself
	// is broken, which is ours to fix, not the conversation's to absorb.
	if violations := ValidateToolPairing(msgs); len(violations) > 0 {
		logging.Error("[ToolPairing] INVARIANT VIOLATION after repair — refusing to build an LLM request",
			"chatID", chatID,
			"thread", thread,
			"violationCount", len(violations),
			"violations", summarizeViolations(violations),
			"messageCount", len(msgs))
		return nil, fmt.Errorf(
			"tool-pairing invariant violated after repair (chat=%s thread=%s): %s",
			chatID, thread, summarizeViolations(violations))
	}

	return msgs, nil
}
