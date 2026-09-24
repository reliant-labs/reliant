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

// SpawnToolCallIDsByChildThread maps child thread id -> the spawn tool call
// that started it, for one chat.
//
// The reconnect snapshot needs this because threads has no
// spawned_by_tool_call_id column: the field only ever rides the live update
// payload, so a client that reloads sees none and the background-work pill
// loses its cancel button. tool_calls.child_workflow_id -> workflows.thread is
// the durable link the spawn path already writes.
func (r *Repo) SpawnToolCallIDsByChildThread(ctx context.Context, chatID string) (map[string]string, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	if r.DB == nil {
		return nil, fmt.Errorf("repository has no database connection")
	}

	rows, err := pgdb.New(r.DB.DB(ctx)).ListSpawnToolCallIDsByChildThread(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list spawn tool call ids: %w", err)
	}

	// Rows arrive requested_at ASC, and a resumed spawn contributes one row
	// per resumption for the same thread. Overwriting therefore leaves the
	// most recent call as the value — the one still executing, and the one a
	// cancel must address.
	byThread := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.ChildThreadID == "" || row.ToolCallID == "" {
			continue
		}
		byThread[row.ChildThreadID] = row.ToolCallID
	}
	return byThread, nil
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

// LiveBackgroundSpawn is one background spawn still open inside a root
// execution — backgrounded, with no terminal report to its parent. See
// ListLiveBackgroundSpawns.
type LiveBackgroundSpawn struct {
	ToolCallID string
	// ParentThreadID is the thread that issued the spawn (the mailbox
	// recipient of its eventual report).
	ParentThreadID    string
	ToolInput         []byte
	ChildWorkflowID   string
	ChildThreadID     string
	IssuingWorkflowID string
	// Depth is 0 for a spawn issued by the root, 1 for one issued by a
	// top-level spawn, and so on.
	Depth int
}

// ListLiveBackgroundSpawns returns every background spawn issued anywhere in
// rootWorkflowID's execution that has not reported back, parents before
// children. This is the durable record the coarse fresh restart relaunches
// from when the root execution died with spawns in flight.
func (r *Repo) ListLiveBackgroundSpawns(ctx context.Context, rootWorkflowID string) ([]*LiveBackgroundSpawn, error) {
	if rootWorkflowID == "" {
		return nil, fmt.Errorf("root workflow ID cannot be empty")
	}
	if r.DB == nil {
		return nil, fmt.Errorf("repository has no database connection")
	}
	rows, err := pgdb.New(r.DB.DB(ctx)).ListLiveBackgroundSpawnsForWorkflow(ctx, rootWorkflowID)
	if err != nil {
		return nil, fmt.Errorf("failed to list live background spawns: %w", err)
	}
	spawns := make([]*LiveBackgroundSpawn, 0, len(rows))
	for _, row := range rows {
		if !row.ParentThreadID.Valid || row.ParentThreadID.String == "" {
			// No recipient to report to; relaunching it would produce a
			// report nobody can be addressed.
			continue
		}
		spawns = append(spawns, &LiveBackgroundSpawn{
			ToolCallID:        row.ToolCallID,
			ParentThreadID:    row.ParentThreadID.String,
			ToolInput:         row.ToolInput,
			ChildWorkflowID:   row.ChildWorkflowID,
			ChildThreadID:     row.ChildThreadID,
			IssuingWorkflowID: row.IssuingWorkflowID,
			Depth:             int(row.Depth),
		})
	}
	return spawns, nil
}
