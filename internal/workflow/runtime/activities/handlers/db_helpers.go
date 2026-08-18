// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"strings"

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

	// Recover REAL tool results before repair invents placeholder ones.
	//
	// A tool result lives in two places: the durable `tool_call_results` row
	// (written by ExecuteTools) and a tool_result content block on a TOOL-role
	// message (written by SaveMessage). History is assembled ONLY from content
	// blocks, so when the row commits and the message does not, the result is
	// durably recorded and completely invisible to the model.
	//
	// repairMessageHistory would then synthesize InterruptedToolResultContent —
	// telling the model the tool's outcome is unknown while the real 262-char
	// answer sits in the database. Observed exactly that on chat 128cf4f5: a
	// completed spawn_status call (tool_calls.status=3) whose result row was
	// written at 20:30:32.786 but whose TOOL message never was.
	//
	// Consulting the durable row first turns a fabricated error into the actual
	// result. Anything still missing afterwards falls through to the synthetic
	// placeholder, which remains the correct answer for a call that genuinely
	// never produced one.
	msgs = recoverPersistedToolResults(ctx, repo, msgs)

	msgs = repairMessageHistory(msgs)

	// Drop assistant messages that carry nothing at all.
	//
	// A blockless assistant row is durable poison: it becomes the tail of the
	// conversation, CallLLM's end-of-history guard sees an assistant message
	// last and refuses to build a request, and the retry ladder re-runs against
	// the same row forever. The chat cannot advance again without hand-editing
	// the database. Measured: 22 such rows on the live database, and the two
	// newest wedged their chats at 24 logged failures each.
	//
	// SaveMessage now refuses to create these (see internal/threads:
	// validateSaveMessageOpts), so this is the recovery half — it exists for
	// rows written BEFORE that guard, and it is what lets an already-wedged
	// chat heal on its next turn instead of staying stuck forever.
	//
	// Safe to drop rather than repair: the row has no text, no tool calls and
	// no thinking, so removing it discards nothing the model could read. It is
	// deliberately NOT deleted from the database — this load is a read path,
	// the transcript keeps whatever the user saw, and a read path that quietly
	// rewrites history is far harder to reason about than one that filters.
	if kept, dropped := dropBlocklessAssistantMessages(msgs); dropped > 0 {
		logging.Warn("[LoadHistory] Dropped blockless assistant message(s) from loaded history",
			"chatID", chatID,
			"thread", thread,
			"dropped", dropped,
			"messageCountBefore", len(msgs),
			"messageCountAfter", len(kept))
		msgs = kept
	}

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

// dropBlocklessAssistantMessages removes assistant messages that carry no
// content, no tool calls and no reasoning — rows that came from a turn which
// produced literally nothing.
//
// Returns the filtered slice and how many were removed. Non-assistant messages
// are never touched: a tool message with no results or a user message with no
// text are different bugs with different repairs, and silently dropping either
// would hide them.
func dropBlocklessAssistantMessages(msgs []message.Message) ([]message.Message, int) {
	dropped := 0
	kept := make([]message.Message, 0, len(msgs))
	for i := range msgs {
		m := &msgs[i]
		if m.Role == message.Assistant && isBlocklessAssistant(m) {
			dropped++
			continue
		}
		kept = append(kept, msgs[i])
	}
	if dropped == 0 {
		// Nothing to do — return the original slice so the common path does
		// not pay for a copy.
		return msgs, 0
	}
	return kept, dropped
}

// isBlocklessAssistant reports whether an assistant message would render as
// zero content blocks. Mirrors the inputs threads.createAssistantContentBlocks
// uses, so the two cannot disagree about what "empty" means.
func isBlocklessAssistant(m *message.Message) bool {
	if strings.TrimSpace(m.Content().String()) != "" {
		return false
	}
	if len(m.ToolCalls()) > 0 {
		return false
	}
	if strings.TrimSpace(m.ReasoningContent().String()) != "" {
		return false
	}
	return true
}

// recoverPersistedToolResults fills in tool results that exist in the durable
// tool_call_results table but never made it into the message history as a
// tool_result content block.
//
// Returns history with a synthetic TOOL message appended for each recovered
// result, placed immediately after the assistant message that made the call so
// the tool-pairing invariant holds. Messages are otherwise untouched.
//
// This runs BEFORE repairMessageHistory so that recovered results win over the
// "unknown outcome" placeholder that repair would otherwise synthesize. A call
// with no durable row is left alone — repair's placeholder is the right answer
// for a tool that genuinely never reported.
func recoverPersistedToolResults(ctx context.Context, repo db.Repository, msgs []message.Message) []message.Message {
	if len(msgs) == 0 || repo == nil {
		return msgs
	}

	// Which tool calls already have a result in history?
	answered := make(map[string]bool)
	for i := range msgs {
		if msgs[i].Role != message.Tool {
			continue
		}
		for _, tr := range msgs[i].ToolResults() {
			answered[tr.ToolCallID] = true
		}
	}

	out := make([]message.Message, 0, len(msgs))
	recovered := 0
	for i := range msgs {
		out = append(out, msgs[i])
		if msgs[i].Role != message.Assistant {
			continue
		}

		var parts []message.ContentPart
		for _, tc := range msgs[i].ToolCalls() {
			if answered[tc.ID] {
				continue
			}
			row, err := repo.GetToolCallResult(ctx, tc.ID)
			if err != nil || row == nil || row.Content == "" {
				// No durable result — leave it for repairMessageHistory, which
				// synthesizes the "outcome unknown" placeholder.
				continue
			}
			parts = append(parts, message.ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    row.Content,
				IsError:    row.IsError,
			})
			answered[tc.ID] = true
			recovered++
		}

		if len(parts) > 0 {
			out = append(out, message.Message{
				ID:    fmt.Sprintf("recovered-tool-result-%s", msgs[i].ID),
				Role:  message.Tool,
				Parts: parts,
			})
		}
	}

	if recovered == 0 {
		return msgs
	}
	logging.Warn("[LoadHistory] Recovered tool result(s) from tool_call_results that were missing from message history",
		"recovered", recovered,
		"messageCountBefore", len(msgs),
		"messageCountAfter", len(out))
	return out
}
