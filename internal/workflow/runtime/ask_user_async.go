// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// An async ask_user (input async: true) asks without waiting. The question is
// recorded like any other — a questions row the timeline renders and the
// user's inbox lists — but flagged async, and the tool returns at once, so
// the agent's loop goes on. Nothing in the run waits for the answer: the
// question service delivers it to the asking thread as a message from the
// user (its mailbox, drained at the thread's next call_llm), which resumes a
// paused run and reaches a finished thread the way any user message does.
// See research/ASYNC_ASK_USER.md.
//
// It is the mailbox and not a signal because the answer may come long after
// the asking loop, or the whole run, has ended; the mailbox already has the
// semantics for that, and a background listener per question would have to
// survive continue-as-new and completion to match them.

// askUserAsyncVersion is the version an async ask_user records. Its change
// id is per tool call (askUserAsyncVersionID) and asked only for a call whose
// input says async, so a history with no such call never records a marker,
// and one that held an async call before the option existed replays as
// DefaultVersion: the blocking wait it took then.
const askUserAsyncVersion workflow.Version = 1

// askUserAsyncChangeID prefixes askUserAsyncVersionID.
const askUserAsyncChangeID = "ask-user-async"

// askUserAsyncVersionID is the change id gating the async path of one
// ask_user tool call.
func askUserAsyncVersionID(toolCallID string) string {
	return fmt.Sprintf("%s/%s", askUserAsyncChangeID, toolCallID)
}

// askUserInputAsync reports whether an ask_user call's parsed input asks to
// post the question without waiting. A quoted boolean counts: tool input
// coercion is the norm elsewhere, and a model that wrote "true" meant it.
func askUserInputAsync(input map[string]interface{}) bool {
	switch v := input["async"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

// askUserQuestionInput is what QuestionCreate records for an ask_user call.
type askUserQuestionInput struct {
	chatID, workflowID, temporalWorkflowID, threadID string
	stepID, loopNodeID                               string
	loopIteration                                    int
	metadata                                         string
}

// askUserAsync records an async question and returns the tool result that
// tells the model to carry on. A QuestionCreate that fails is reported as the
// tool's error, as for a blocking ask: the model can retry or proceed.
func askUserAsync(ctx workflow.Context, toolCallID string, in askUserQuestionInput, logger log.Logger) map[string]interface{} {
	createCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	var created struct {
		QuestionID string `json:"question_id"`
	}
	if err := workflow.ExecuteActivity(createCtx, "QuestionCreate", map[string]interface{}{
		"chat_id":              in.chatID,
		"workflow_id":          in.workflowID,
		"temporal_workflow_id": in.temporalWorkflowID,
		"thread_id":            in.threadID,
		"step_id":              in.stepID,
		"loop_node_id":         in.loopNodeID,
		"loop_iteration":       in.loopIteration,
		"metadata":             in.metadata,
		"tool_call_id":         toolCallID,
		"async":                true,
	}).Get(ctx, &created); err != nil {
		logger.Error("[AskUser] QuestionCreate failed for an async question", "error", err, "toolCallID", toolCallID)
		return map[string]interface{}{
			"tool_call_id": toolCallID,
			"content":      fmt.Sprintf("ask_user failed: %v", err),
			"is_error":     true,
		}
	}
	logger.Info("[AskUser] Posted an async question; continuing", "questionID", created.QuestionID, "toolCallID", toolCallID)
	return map[string]interface{}{
		"tool_call_id": toolCallID,
		"content":      asyncAskUserResponse(created.QuestionID),
	}
}

// asyncAskUserResponse is the tool result of an async ask_user: the question
// is posted, and the model must not wait for it.
func asyncAskUserResponse(questionID string) string {
	return fmt.Sprintf("Question posted to the user without waiting (question_id: %s). "+
		"Do not wait for the answer and do not ask it again: continue your work now, proceeding on your best judgment "+
		"(the option you would recommend). If the user answers, the answer arrives later as a message from the user; "+
		"adjust your work then if it changes anything.", questionID)
}
