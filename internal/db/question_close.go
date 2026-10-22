// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/store"
)

// A THREAD THAT FINISHED HAS NO PENDING QUESTION.
//
// An ask_user (or ask_question) wait lives in the asking thread's run, so a
// question outlives a thread that finished only as a gate nothing is behind:
// the composer keeps offering it, the chat reads as awaiting input, and an
// answer is signalled into a channel no one reads. The run-end cleanup denied
// the chat's pending approvals but left its questions alone.
//
// So every write that finishes a thread closes the thread's pending questions
// with it — UpdateThreadStatus (the thread's own end), the terminal cascade
// (CascadeTerminalStatusToThreadSubtree), and the reaper's repair
// (ReapOrphanedThreads) — the way the cascade closes the threads themselves:
// at the data layer, so no write path can forget. Closing is resolving with no
// answer, guarded by status = pending (an answer that got there first is
// kept), and announced on the chat's feed so clients drop the gate.
//
// FAILED is deliberately not an end here (threadEndClosesQuestions). A failed
// run is resumable, and its resume puts the wait back: reset-and-replay
// rebuilds the run parked on the same question, and SendMessage's precise
// resume of a run that died on an unanswered question (resumeFailedQuestion
// Workflow) delivers the user's message as that question's answer — both need
// the row still pending. A stopped sub-agent resumed by a message is the same
// shape. The operator terminate, which starts the chat fresh, voids every
// pending question explicitly (runs.Service.voidPendingQuestions).
//
// An async question (ask_user async: true) is none of this: no wait is behind
// it, so a thread's end leaves it pending. It is an item in the user's inbox,
// and its answer is delivered to the thread as a message, which resumes or
// restarts what it must (research/ASYNC_ASK_USER.md).

// threadEndClosesQuestions reports whether a thread reaching status has
// finished for good, so its pending questions can never be answered.
func threadEndClosesQuestions(status ThreadStatus) bool {
	return status == core.ThreadStatusCompleted || status == core.ThreadStatusCancelled
}

// closePendingQuestionsInWorkflowSubtree closes the pending questions of a
// workflow subtree that finished.
func (r *Repo) closePendingQuestionsInWorkflowSubtree(ctx context.Context, workflowID string) (int, error) {
	closed, err := store.ClosePendingQuestionsInWorkflowSubtree(ctx, r.ORM(), workflowID, nil)
	if err != nil {
		return 0, fmt.Errorf("close pending questions of workflow %s: %w", workflowID, err)
	}
	r.announceClosedQuestions(ctx, closed)
	return len(closed), nil
}

// closePendingQuestionsOfThreads closes the pending questions of threads
// that finished. It returns the error for callers inside a transaction, whose
// statement failure must abort it.
func (r *Repo) closePendingQuestionsOfThreads(ctx context.Context, threadIDs ...string) error {
	closed, err := store.ClosePendingQuestionsForThreads(ctx, r.ORM(), threadIDs, nil)
	if err != nil {
		return fmt.Errorf("close pending questions of finished threads: %w", err)
	}
	r.announceClosedQuestions(ctx, closed)
	return nil
}

// announceClosedQuestions tells each closed question's chat it is resolved.
// Best-effort: the row is what the gates read; the feed is what clients
// render, and a missed update costs a stale prompt, not a stuck run.
func (r *Repo) announceClosedQuestions(ctx context.Context, closed []store.ClosedQuestion) {
	chats := make(map[string]bool)
	for _, q := range closed {
		if err := r.EmitQuestionUpdate(ctx, q.ChatID, QuestionUpdate{
			QuestionID: q.ID,
			ChatID:     q.ChatID,
			WorkflowID: q.WorkflowID,
			ThreadID:   q.ThreadID,
			StepID:     q.StepID,
			Status:     "resolved",
		}); err != nil {
			logging.WarnContext(ctx, "Failed to announce a closed question", "questionID", q.ID, "chatID", q.ChatID, "error", err)
		}
		chats[q.ChatID] = true
	}
	for chatID := range chats {
		_ = r.emitChatActivityIfChanged(ctx, chatID)
	}
}
