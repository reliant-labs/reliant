// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/store"
)

// An agent's questions are those of its own thread and every sub-agent below
// it (store.threadSubtreeCTE): list_pending_questions shows them, and
// withdraw_question retracts an async one that has become moot, so the
// user's inbox does not fill with stale questions.

// SubtreeQuestion is a pending question in a thread subtree.
type SubtreeQuestion = store.SubtreeQuestion

// WithdrawOutcome is what WithdrawAsyncQuestion did.
type WithdrawOutcome = store.WithdrawOutcome

const (
	QuestionWithdrawn        = store.Withdrawn
	QuestionWithdrawNotFound = store.WithdrawNotFound
	QuestionWithdrawBlocking = store.WithdrawBlocking
)

// ListPendingQuestionsInThreadSubtree returns the pending questions — async
// and blocking — asked on threadID or any thread below it in chatID, oldest
// first.
func (r *Repo) ListPendingQuestionsInThreadSubtree(ctx context.Context, chatID, threadID string) ([]SubtreeQuestion, error) {
	if chatID == "" || threadID == "" {
		return nil, fmt.Errorf("chat ID and thread ID are required")
	}
	return store.ListPendingQuestionsInThreadSubtree(ctx, r.ORM(), chatID, threadID)
}

// WithdrawAsyncQuestion withdraws the pending async question questionID if it
// was asked on threadID or below it: resolved with no answer and the reason,
// announced on the chat's feed (the timeline card shows the reason), and the
// user's inbox told to re-read. A blocking question is refused: its agent is
// parked on it, and only the user's answer or that agent ending closes it.
func (r *Repo) WithdrawAsyncQuestion(ctx context.Context, chatID, threadID, questionID, reason string) (WithdrawOutcome, error) {
	if chatID == "" || threadID == "" || questionID == "" {
		return 0, fmt.Errorf("chat ID, thread ID and question ID are required")
	}
	outcome, closed, err := store.WithdrawAsyncQuestion(ctx, r.ORM(), chatID, threadID, questionID, reason)
	if err != nil || outcome != QuestionWithdrawn {
		return outcome, err
	}

	// Best-effort, as for every closed question: the row is what the gates
	// and the inbox read; the feed is what clients render.
	if err := r.EmitQuestionUpdate(ctx, closed.ChatID, QuestionUpdate{
		QuestionID:      closed.ID,
		ChatID:          closed.ChatID,
		WorkflowID:      closed.WorkflowID,
		ThreadID:        closed.ThreadID,
		StepID:          closed.StepID,
		Status:          "resolved",
		Async:           true,
		WithdrawnReason: reason,
	}); err != nil {
		logging.WarnContext(ctx, "Failed to announce a withdrawn question", "questionID", closed.ID, "error", err)
	}
	if chat, err := r.GetChat(ctx, closed.ChatID); err == nil && chat != nil {
		if err := r.EmitUserRefetch(ctx, chat.UserID, RefetchInbox, RefetchOpts{ChatID: &closed.ChatID}); err != nil {
			logging.WarnContext(ctx, "Failed to tell the inbox a question was withdrawn", "questionID", closed.ID, "error", err)
		}
	}
	return outcome, nil
}
