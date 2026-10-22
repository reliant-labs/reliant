// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A question is only ever answerable while the thread that asked it is
// waiting on it: the wait lives in that thread's run. Once the thread ends, a
// pending row is a gate nothing is behind — the UI keeps offering it, the
// chat reads as awaiting input, and an answer is signalled into a channel no
// one reads. These close such rows in bulk, each guarded by status = pending,
// so an answer that got there first is never overwritten.

// ClosedQuestion is a question one of these statements resolved: what a
// caller needs to announce it.
type ClosedQuestion struct {
	ID         string `bun:"id"`
	ChatID     string `bun:"chat_id"`
	WorkflowID string `bun:"workflow_id"`
	ThreadID   string `bun:"thread_id"`
	StepID     string `bun:"step_id"`
}

const closeQuestionsSet = `
UPDATE questions AS q
SET status = ?0::int,
    response_data = ?1,
    resolved_at = NOW()`

const closeQuestionsReturning = `
RETURNING q.id, q.chat_id, q.workflow_id, q.thread_id, q.step_id`

// closePendingQuestionsForThreadsSQL closes the pending questions asked on
// any of the given threads.
const closePendingQuestionsForThreadsSQL = closeQuestionsSet + `
WHERE q.status = ?2::int
  AND q.thread_id IN (?3)` + closeQuestionsReturning

// closePendingQuestionsInWorkflowSubtreeSQL closes the pending questions of
// a workflow and every workflow under it: those recorded against one of the
// subtree's workflows, and those asked on a thread one of them owns. The
// subtree walk is the one the terminal cascade uses
// (cascadeTerminalStatusToThreadSubtreeSQL).
const closePendingQuestionsInWorkflowSubtreeSQL = `
WITH RECURSIVE subtree AS (
    SELECT ?3::text AS id
    UNION ALL
    SELECT c.id FROM workflows c JOIN subtree s ON c.parent_id = s.id
)` + closeQuestionsSet + `
WHERE q.status = ?2::int
  AND (q.workflow_id IN (SELECT id FROM subtree)
       OR q.thread_id IN (SELECT t.id FROM threads t JOIN subtree s ON t.workflow_id = s.id))` + closeQuestionsReturning

// ClosePendingQuestionsForThreads resolves, with responseData, every pending
// question asked on one of threadIDs.
func ClosePendingQuestionsForThreads(ctx context.Context, db orm.Context, threadIDs []string, responseData *string) ([]ClosedQuestion, error) {
	if len(threadIDs) == 0 {
		return nil, nil
	}
	return closeQuestions(ctx, db, closePendingQuestionsForThreadsSQL, responseData, bun.List(threadIDs))
}

// ClosePendingQuestionsInWorkflowSubtree resolves, with responseData, every
// pending question of workflowID's subtree.
func ClosePendingQuestionsInWorkflowSubtree(ctx context.Context, db orm.Context, workflowID string, responseData *string) ([]ClosedQuestion, error) {
	return closeQuestions(ctx, db, closePendingQuestionsInWorkflowSubtreeSQL, responseData, workflowID)
}

func closeQuestions(ctx context.Context, db orm.Context, query string, responseData *string, scope interface{}) ([]ClosedQuestion, error) {
	var closed []ClosedQuestion
	if err := db.Bun().NewRaw(query,
		int32(core.QuestionStatusResolved), responseData, int32(core.QuestionStatusPending), scope,
	).Scan(ctx, &closed); err != nil {
		return nil, err
	}
	return closed, nil
}
