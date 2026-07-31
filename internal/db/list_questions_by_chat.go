// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
)

// OpenReadOnlyRepo opens a Postgres connection and wraps it in a *Repo WITHOUT
// running migrations. It is intended for read-only tooling (run analysis /
// node inspection) that must not mutate the target database: the normal
// NewRepoFromConfig path applies migrations on connect, which is a write.
//
// The caller owns the returned Repo and must Close() it. The pgx stdlib driver
// is registered by this package's connect.go import, so callers need not import
// it themselves.
func OpenReadOnlyRepo(url string) (*Repo, error) {
	if url == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres database: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to connect to postgres database: %w", err)
	}
	return NewRepoWithDriver(sqlDB, DriverPostgres), nil
}

// ListQuestionsByChat returns every question (gate) raised for a chat, in
// creation order. It is a read-only query used by run-analysis tooling
// (`reliant-dev workflow analyze`) to reconstruct the gate/question history —
// what was asked at each checkpoint and what was answered.
//
// It mirrors the scan shape of the other question queries in
// repository_impl.go and adds no new mutation surface.
func (r *Repo) ListQuestionsByChat(ctx context.Context, chatID string) ([]*Question, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	query := `SELECT id, chat_id, workflow_id, temporal_workflow_id, thread_id, step_id, loop_node_id, loop_iteration, status, metadata, response_data, created_at, resolved_at, tool_call_id
		FROM questions WHERE chat_id = ? ORDER BY created_at ASC`
	query = r.bindQuery(query)

	rows, err := r.DB.DB(ctx).QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list questions by chat: %w", err)
	}
	defer rows.Close()

	var questions []*Question
	for rows.Next() {
		var q Question
		if err := rows.Scan(
			&q.ID, &q.ChatID, &q.WorkflowID, &q.TemporalWorkflowID, &q.ThreadID, &q.StepID,
			&q.LoopNodeID, &q.LoopIteration, &q.Status, &q.Metadata, &q.ResponseData,
			&q.CreatedAt, &q.ResolvedAt, &q.ToolCallID,
		); err != nil {
			return nil, fmt.Errorf("failed to scan question: %w", err)
		}
		questions = append(questions, &q)
	}
	return questions, rows.Err()
}

// PendingQuestionsByThread returns each thread's newest PENDING question for a
// chat, keyed by the question's thread id — which is exactly a workflow row's
// `thread` column, so the result joins straight onto workflow rows.
//
// It exists because GetPendingQuestionByChatID cannot answer "which thread is
// waiting?". That query selects thread_id and workflow_id and then throws the
// scoping away with `WHERE chat_id = ? ... ORDER BY created_at DESC LIMIT 1`,
// so in a chat with several spawned threads it hands back the newest question
// from ANY thread. A caller that reports per thread — `reliant-dev workflow ps` —
// would then stamp one thread's gate onto siblings that are genuinely
// executing. Keying by thread here makes that smear impossible at the source
// rather than asking every caller to avoid it.
//
// Chat-wide questions ("is this chat awaiting input at all?", which is what the
// reconciler asks) are still GetPendingQuestionByChatID's job.
//
// Read-only: one SELECT per chat, no mutation.
func (r *Repo) PendingQuestionsByThread(ctx context.Context, chatID string) (map[string]*Question, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}

	query := r.bindQuery(`SELECT id, chat_id, workflow_id, temporal_workflow_id, thread_id, step_id, loop_node_id, loop_iteration, status, metadata, response_data, created_at, resolved_at, tool_call_id
		FROM questions WHERE chat_id = ? AND status = 1 ORDER BY created_at ASC`)

	rows, err := r.DB.DB(ctx).QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pending questions by chat: %w", err)
	}
	defer rows.Close()

	byThread := map[string]*Question{}
	for rows.Next() {
		var q Question
		if err := rows.Scan(
			&q.ID, &q.ChatID, &q.WorkflowID, &q.TemporalWorkflowID, &q.ThreadID, &q.StepID,
			&q.LoopNodeID, &q.LoopIteration, &q.Status, &q.Metadata, &q.ResponseData,
			&q.CreatedAt, &q.ResolvedAt, &q.ToolCallID,
		); err != nil {
			return nil, fmt.Errorf("failed to scan pending question: %w", err)
		}
		// Ascending order means a later row is the newer question for its thread.
		byThread[q.ThreadID] = &q
	}
	return byThread, rows.Err()
}
