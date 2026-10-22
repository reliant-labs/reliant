// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// An agent's questions are the ones asked on its own thread or on any
// sub-agent below it, recursively (threads.parent_thread_id): what it is
// responsible for, and never a sibling's business. list_pending_questions
// lists them and withdraw_question retracts one, both through this walk.
//
// The walk follows the thread tree rather than the workflows tree the
// terminal cascade walks: a thread is the agent, while a workflows row is one
// run of it (a resumed sub-agent gets a new row on the same thread), and the
// question rows name their thread. UNION, not UNION ALL, so a malformed cycle
// ends the walk instead of looping; the chat scope keeps it inside the chat.
const threadSubtreeCTE = `
WITH RECURSIVE subtree AS (
    SELECT ?0::text AS id
    UNION
    SELECT t.id FROM threads t JOIN subtree s ON t.parent_thread_id = s.id
    WHERE t.chat_id = ?1
)`

// SubtreeQuestion is a pending question in a thread subtree, with the title
// of the thread that asked it.
type SubtreeQuestion struct {
	ID          string    `bun:"id"`
	ChatID      string    `bun:"chat_id"`
	WorkflowID  string    `bun:"workflow_id"`
	ThreadID    string    `bun:"thread_id"`
	StepID      string    `bun:"step_id"`
	Async       bool      `bun:"async"`
	Metadata    *string   `bun:"metadata"`
	ToolCallID  *string   `bun:"tool_call_id"`
	CreatedAt   time.Time `bun:"created_at"`
	ThreadTitle *string   `bun:"thread_title"`
}

const listPendingQuestionsInThreadSubtreeSQL = threadSubtreeCTE + `
SELECT q.id, q.chat_id, q.workflow_id, q.thread_id, q.step_id, q.async,
       q.metadata, q.tool_call_id, q.created_at, t.title AS thread_title
FROM questions q
LEFT JOIN threads t ON t.id = q.thread_id
WHERE q.chat_id = ?1
  AND q.status = ?2::int
  AND q.thread_id IN (SELECT id FROM subtree)
ORDER BY q.created_at ASC, q.id ASC`

// ListPendingQuestionsInThreadSubtree returns the pending questions — async
// and blocking — asked on threadID or any thread below it in chatID, oldest
// first.
func ListPendingQuestionsInThreadSubtree(ctx context.Context, db orm.Context, chatID, threadID string) ([]SubtreeQuestion, error) {
	var rows []SubtreeQuestion
	if err := db.Bun().NewRaw(listPendingQuestionsInThreadSubtreeSQL,
		threadID, chatID, int32(core.QuestionStatusPending)).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list pending questions in thread subtree: %w", err)
	}
	return rows, nil
}

// WithdrawOutcome is what WithdrawAsyncQuestion did.
type WithdrawOutcome int

const (
	// Withdrawn: the question was pending and async, and is now resolved
	// with the reason.
	Withdrawn WithdrawOutcome = iota
	// WithdrawNotFound: no pending question by that id in the subtree —
	// none at all, someone else's, or already answered or closed.
	WithdrawNotFound
	// WithdrawBlocking: the question is a blocking one. Its agent is parked
	// on it, and only the user's answer (or that agent ending) closes it.
	WithdrawBlocking
)

const findPendingQuestionInThreadSubtreeSQL = threadSubtreeCTE + `
SELECT q.async
FROM questions q
WHERE q.id = ?2
  AND q.chat_id = ?1
  AND q.status = ?3::int
  AND q.thread_id IN (SELECT id FROM subtree)`

// withdrawAsyncQuestionSQL resolves the question with no answer and the
// reason, guarded by status = pending and async: an answer the user sent
// first is kept, and a blocking question is never touched.
const withdrawAsyncQuestionSQL = `
UPDATE questions AS q
SET status = ?0::int,
    withdrawn_reason = ?1,
    resolved_at = NOW()
WHERE q.id = ?2
  AND q.status = ?3::int
  AND q.async
RETURNING q.id, q.chat_id, q.workflow_id, q.thread_id, q.step_id`

// WithdrawAsyncQuestion withdraws the pending async question questionID if
// it was asked on threadID or below it in chatID. The closed row is returned
// when it was withdrawn.
func WithdrawAsyncQuestion(ctx context.Context, db orm.Context, chatID, threadID, questionID, reason string) (WithdrawOutcome, *ClosedQuestion, error) {
	var async bool
	err := db.Bun().NewRaw(findPendingQuestionInThreadSubtreeSQL,
		threadID, chatID, questionID, int32(core.QuestionStatusPending)).Scan(ctx, &async)
	if errors.Is(err, sql.ErrNoRows) {
		return WithdrawNotFound, nil, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("failed to find question %s: %w", questionID, err)
	}
	if !async {
		return WithdrawBlocking, nil, nil
	}

	var closed []ClosedQuestion
	if err := db.Bun().NewRaw(withdrawAsyncQuestionSQL,
		int32(core.QuestionStatusResolved), reason, questionID, int32(core.QuestionStatusPending),
	).Scan(ctx, &closed); err != nil {
		return 0, nil, fmt.Errorf("failed to withdraw question %s: %w", questionID, err)
	}
	if len(closed) == 0 {
		// Answered between the read and the write: the answer stands.
		return WithdrawNotFound, nil, nil
	}
	return Withdrawn, &closed[0], nil
}
