// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

// SpawnChild is one spawn call a thread has issued, joined to the state of
// the child it names. ChildThreadID/WorkflowStatus/ThreadTitle are nil when
// the child's workflow+thread rows have not landed yet (a narrow window
// right after dispatch, before CreateWorkflowWithThread's activity commits) —
// callers should treat that as "still starting", not as an error.
type SpawnChild struct {
	ToolCallID        string
	ToolCallStatus    int32
	ToolInput         []byte
	RequestedAt       time.Time
	CompletedAt       *time.Time
	ChildThreadID     *string
	WorkflowStatus    *WorkflowStatus
	WorkflowCompleted *time.Time
	ThreadTitle       *string
}

// ListSpawnChildren returns every spawn call issued BY threadID, in request
// order. threadID must be the CALLER's own thread — tool_calls.thread_id is
// always the parent's, so this cannot return another thread's children by
// construction; it is the caller's job to pass its own thread id, not an
// arbitrary one.
func (r *Repo) ListSpawnChildren(ctx context.Context, threadID string) ([]*SpawnChild, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	if r.DB == nil {
		return nil, fmt.Errorf("repository has no database connection")
	}

	dbtx := r.DB.DB(ctx)
	rows, err := pgdb.New(dbtx).ListSpawnChildrenForThread(ctx, sql.NullString{String: threadID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list spawn children: %w", err)
	}

	children := make([]*SpawnChild, 0, len(rows))
	for _, row := range rows {
		child := &SpawnChild{
			ToolCallID:     row.ToolCallID,
			ToolCallStatus: row.ToolCallStatus,
			ToolInput:      row.ToolInput,
			RequestedAt:    row.RequestedAt,
		}
		if row.CompletedAt.Valid {
			t := row.CompletedAt.Time
			child.CompletedAt = &t
		}
		if row.ChildThreadID.Valid {
			s := row.ChildThreadID.String
			child.ChildThreadID = &s
		}
		// state and stop_reason arrive from the same LEFT JOIN, so either both
		// are present or the child's workflow row has not landed yet.
		if row.WorkflowState.Valid {
			status := WorkflowStatus{
				State:      WorkflowState(row.WorkflowState.Int32),
				StopReason: WorkflowStopReason(row.WorkflowStopReason.Int32),
			}
			child.WorkflowStatus = &status
		}
		if row.WorkflowCompletedAt.Valid {
			t := row.WorkflowCompletedAt.Time
			child.WorkflowCompleted = &t
		}
		if row.ThreadTitle.Valid {
			s := row.ThreadTitle.String
			child.ThreadTitle = &s
		}
		children = append(children, child)
	}
	return children, nil
}
