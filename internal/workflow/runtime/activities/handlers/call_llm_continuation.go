// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"strings"

	"go.temporal.io/sdk/activity"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/stopreason"
)

// Harness continuations: turns the harness continues with a hidden nudge
// (CallLLMOutput.continuation, handed back as CallLLMArgs.continuation).
//
// Two cases, both turns that ended with work left to do:
//
//   - truncated: the turn hit its output cap. agent.yaml always re-entered on
//     stop_reason "truncated", but the next call found history ending with the
//     assistant and yielded without calling the provider, so the turn ended as
//     "done" with half an answer. Now the next call appends a nudge and calls
//     the provider, up to maxTruncationContinuations in a row; at the cap the
//     turn ends as "truncated".
//   - commentary / reasoning_only: an OpenAI Responses turn (codex, openai)
//     that the provider called completed, with no tool call, ending on
//     interim commentary or on reasoning alone (llm.PrematureStop). It gets one
//     nudged retry; if the retry stops the same way, the turn ends as done.
//
// Every nudge is a USER message saved with display_style HIDDEN, the shape
// resumeCutOffTurn already uses: the model reads it, the transcript the user
// sees is unchanged, and the conversation stays well-formed for later turns
// (no assistant-tailed history, which every provider handles differently;
// Responses has no assistant prefill, and Anthropic refuses one when thinking
// is on). The same nudge works for every provider, so there is one shape.
//
// The counts are read back from those saved nudges, so they live in the
// thread itself: deterministic for a replay, and nothing new in workflow
// state. A tool call, a tool result or new input ends a run of nudges and
// resets both counts.
const (
	continuationTruncated     = "truncated"
	continuationCommentary    = string(llm.PrematureStopCommentary)
	continuationReasoningOnly = string(llm.PrematureStopReasoningOnly)
)

// maxTruncationContinuations caps nudged continuations of truncated turns in a
// row. opencode uses the same cap for the same case.
const maxTruncationContinuations = 3

// The nudge texts. They are matched exactly when counting (isContinuationNudge),
// so changing one only means a nudge saved under the old text is not counted.
const (
	truncationNudgeText = "Your previous response hit the output token limit. Continue from where you stopped. " +
		"Do not apologize or repeat what you already wrote, and break the remaining work into smaller steps."
	commentaryNudgeText = "[System: Continue now. Execute the required tool calls and only send your final answer " +
		"after completing the task.]"
	reasoningOnlyNudgeText = "Your previous response contained only internal reasoning and never produced a visible " +
		"answer or tool call. Produce your final answer now, or make the tool call you were planning."
)

// continuationNudgeText returns the nudge for a continuation kind, and false
// for a kind this runtime does not know.
func continuationNudgeText(kind string) (string, bool) {
	switch kind {
	case continuationTruncated:
		return truncationNudgeText, true
	case continuationCommentary:
		return commentaryNudgeText, true
	case continuationReasoningOnly:
		return reasoningOnlyNudgeText, true
	}
	return "", false
}

// requestedContinuation is the continuation this call should perform: the
// node's continuation arg when it names a known kind, and "" when the mailbox
// delivered new input this turn (newInput) — that input is what the model
// answers, and a nudge after it would talk over the user.
func requestedContinuation(args *reliantv1.CallLLMArgs, newInput bool) string {
	if newInput {
		return ""
	}
	kind := strings.TrimSpace(model.CelStringValue(args.GetContinuation()))
	if _, ok := continuationNudgeText(kind); !ok {
		return ""
	}
	return kind
}

// isContinuationNudge reports which nudge m is, if it is one of ours.
func isContinuationNudge(m message.Message) (string, bool) {
	if !m.Hidden || m.Role != message.User {
		return "", false
	}
	var text strings.Builder
	for _, part := range m.TextContents() {
		text.WriteString(part.Text)
	}
	got := strings.TrimSpace(text.String())
	for _, kind := range []string{continuationTruncated, continuationCommentary, continuationReasoningOnly} {
		if nudge, _ := continuationNudgeText(kind); got == nudge {
			return kind, true
		}
	}
	return "", false
}

// trailingNudges counts the nudges of each kind in the run that history ends
// with: walking back past system framing, text-only assistant turns and our
// own nudges, and stopping at anything else (new input, a tool result, an
// assistant turn that called tools). That stop is the reset.
func trailingNudges(history []message.Message) map[string]int {
	counts := map[string]int{}
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if kind, ok := isContinuationNudge(m); ok {
			counts[kind]++
			continue
		}
		if m.Role == message.System || (m.Role == message.Assistant && len(m.ToolCalls()) == 0) {
			continue
		}
		break
	}
	return counts
}

// planContinuation decides, from a finished turn, whether the harness
// continues it, and returns the continuation kind ("" for none). history is
// the request this turn sent, nudge included.
//
// stop_reason is never changed here. A capped truncation is reported as
// "truncated", and a premature stop stays "done", the provider's verdict: the
// continuation field is what carries the retry, so a loop that predates it
// ends exactly as it did before.
func planContinuation(stopReason string, premature llm.PrematureStop, requested string, history []message.Message) string {
	switch {
	case stopReason == stopreason.Truncated:
		if trailingNudges(history)[continuationTruncated] < maxTruncationContinuations {
			return continuationTruncated
		}
	case stopReason == stopreason.Done && premature != "":
		kind := string(premature)
		if _, ok := continuationNudgeText(kind); !ok {
			return ""
		}
		nudges := trailingNudges(history)
		spent := requested == continuationCommentary || requested == continuationReasoningOnly ||
			nudges[continuationCommentary] > 0 || nudges[continuationReasoningOnly] > 0
		if !spent {
			return kind
		}
	}
	return ""
}

// logContinuationPlan records a continuation decision worth seeing: one that
// continues, and a cap that ends a turn early.
func logContinuationPlan(ctx context.Context, chatID, thread, stopReason string, premature llm.PrematureStop, requested, continuation string) {
	switch {
	case continuation != "":
		logging.InfoContext(ctx, "[CallLLM] Continuing the turn with a hidden nudge",
			"chatID", chatID, "thread", thread, "stopReason", stopReason,
			"continuation", continuation, "requested", requested)
	case stopReason == stopreason.Truncated:
		logging.WarnContext(ctx, "[CallLLM] Turn truncated again after the continuation cap; ending it as truncated",
			"chatID", chatID, "thread", thread, "cap", maxTruncationContinuations)
	case premature != "":
		logging.InfoContext(ctx, "[CallLLM] Premature stop after its one nudged retry; ending the turn",
			"chatID", chatID, "thread", thread, "prematureStop", string(premature), "requested", requested)
	}
}

// appendContinuationNudge appends the nudge unless history already holds it:
// a retried attempt of this activity reloads the row the first attempt saved
// (same idempotency key, so the same id), and sending it twice would be a
// different request from the one the first attempt made.
func appendContinuationNudge(history []message.Message, nudge message.Message) []message.Message {
	if nudge.ID != "" {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].ID == nudge.ID {
				return history
			}
		}
	}
	return append(history, nudge)
}

// continuationNudge saves the hidden nudge for kind and returns it for this
// request. Saved, not only sent, so later turns stay well-formed and the
// counts above can read it back. Keyed on the turn's position, so a retry of
// this activity finds the row it already wrote. A failed save is logged and
// the nudge still goes on this request, exactly as in resumeCutOffTurn.
func (a *CallLLMActivity) continuationNudge(ctx context.Context, rtx RuntimeContext, chatID, thread, kind string) message.Message {
	text, _ := continuationNudgeText(kind)
	nudge := message.Message{
		Role:   message.User,
		Hidden: true,
		Parts:  []message.ContentPart{message.TextContent{Text: text}},
	}
	key := rtx.MessageIdempotencyKey
	if key == "" && activity.IsActivity(ctx) {
		info := activity.GetInfo(ctx)
		key = info.WorkflowExecution.ID + "-" + info.ActivityID
	}
	key += "|continuation-" + kind
	var workflowID *string
	if rtx.WorkflowID != "" {
		workflowID = &rtx.WorkflowID
	}
	saved, err := threads.NewService(a.repo).SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:       chatID,
		Thread:       thread,
		Role:         int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
		Content:      text,
		WorkflowID:   workflowID,
		StepID:       rtx.StepID,
		ActivityID:   &key,
		DisplayStyle: int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN),
	})
	if err != nil {
		logging.WarnContext(ctx, "[CallLLM] Could not save the hidden continuation nudge; sending it anyway",
			"chatID", chatID, "thread", thread, "continuation", kind, "error", err)
		return nudge
	}
	if saved != nil {
		nudge.ID = saved.MessageID
	}
	return nudge
}
