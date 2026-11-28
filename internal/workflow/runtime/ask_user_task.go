// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/askuser"
)

// A task is an ask_user call with task: true: a to-do for the user rather
// than a question — something only they can do (sign in, approve in another
// system, provide a secret). It is the same questions row, its metadata
// flagged "task": true, and each of its questions[] is one item. The user
// answers each item Done or Not done (askuser.TaskDone / TaskNotDone), with an
// optional note, in the ordinary answers shape, so every resolver path is the
// question's. task is orthogonal to blocking: a task is async by default, and
// blocking: true waits for the user to mark it. Only the wording the model
// reads differs, and only when task is set. See research/ASYNC_ASK_USER.md.

// The tool results of a task whose blocking wait ended without the user
// marking it.
const (
	askUserTaskTimedOut = "The user did not mark the task done or not done in time."

	askUserTaskYieldedToMessage = "The user did not mark this task done or not done. They sent you a message instead, " +
		"which follows in the conversation. Read it and respond to it; if the task still needs doing, you may give it again."
	askUserTaskYieldedToInterrupt = "The user interrupted this task without marking it done or not done. Check the " +
		"conversation for anything new from them and continue from there; give the task again only if it still needs doing."
)

// asyncAskUserTaskResponse is the tool result of an async task: it is on the
// user's to-do list, and the model must not wait for it.
func asyncAskUserTaskResponse(questionID string) string {
	return fmt.Sprintf("Task posted to the user's to-do list without waiting (question_id: %s). "+
		"Do not wait for it and do not give it again: continue with whatever does not depend on it. "+
		"When the user marks it done or not done, you get a message from the user saying so, with any note or reason they gave; "+
		"carry on from there. If your work makes the task unnecessary first, withdraw it (withdraw_question).", questionID)
}

// formatAskUserResult is the tool result of an answered blocking ask_user:
// formatAskUserResponse for a question, formatAskUserTaskResponse for a task.
func formatAskUserResult(action, responseData string, task bool) string {
	if task {
		return formatAskUserTaskResponse(action, responseData)
	}
	return formatAskUserResponse(action, responseData)
}

// formatAskUserTaskResponse renders the user's marks on a task's items for
// the model: each item's text and whether it was done, with the user's note
// or reason.
func formatAskUserTaskResponse(action, responseData string) string {
	if action == "continue" {
		return "The user continued without marking the task done or not done."
	}
	if responseData == "" {
		return "The user replied via chat message instead of marking the task. Check the conversation for their response."
	}
	var parsed struct {
		Answers []askuser.Answer `json:"answers"`
	}
	if err := json.Unmarshal([]byte(responseData), &parsed); err != nil || len(parsed.Answers) == 0 {
		// Not structured: the user's own words.
		return responseData
	}
	parts := make([]string, 0, len(parsed.Answers))
	for _, a := range parsed.Answers {
		outcome := a.TaskOutcome()
		if outcome == "" {
			outcome = "The user did not mark it."
		}
		parts = append(parts, fmt.Sprintf("Task: %s\n%s", a.Question, outcome))
	}
	return strings.Join(parts, "\n\n")
}
