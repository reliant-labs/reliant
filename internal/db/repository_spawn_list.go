// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

// SpawnChild is one spawn call a thread has issued, joined to the state of
// the child it names. ChildThreadID/ChildWorkflowID/WorkflowStatus/ThreadTitle
// are nil when the child's workflow+thread rows have not landed yet (a narrow
// window right after dispatch, before CreateWorkflowWithThread's activity
// commits) — callers should treat that as "still starting", not as an error.
//
// ChildThreadID and ChildWorkflowID are BOTH carried because they are the same
// value only for a first spawn. A resumed spawn (spawn with agent_id) keeps the
// original thread id and gets a fresh workflow row per resumption, so code that
// addresses a spawn needs to know which identity it is holding: the thread is
// what a cancel signal names, the workflow row id is what a status write must
// target. Neither is derivable from the other.
type SpawnChild struct {
	ToolCallID        string
	ToolCallStatus    int32
	ToolInput         []byte
	RequestedAt       time.Time
	CompletedAt       *time.Time
	ChildThreadID     *string
	ChildWorkflowID   *string
	WorkflowStatus    *WorkflowStatus
	WorkflowCompleted *time.Time
	ThreadTitle       *string
	// IssuingMessageOrdinal is the ordinal, in the issuing thread, of the
	// assistant message that made this spawn call. Nil when the call was
	// recorded before that message was finalized. It is what decides whether
	// a chat branched from the issuer inherited the spawn.
	IssuingMessageOrdinal *int64
}

// InheritedSpawnChild is a sub-agent spawned by a conversation the caller's
// chat was BRANCHED from, before the branch point.
//
// A branch copies the transcript, spawn calls included, but not ownership:
// the spawn's tool_calls row still names the original thread, and that thread
// keeps running, keeps receiving the agent's reports, and is the only one that
// can message or stop it. Exactly one owner at a time is what stops two
// orchestrators from steering the same agent, so an inherited child is
// something a branch can SEE, never control.
type InheritedSpawnChild struct {
	SpawnChild
	// SourceThreadID is the thread that issued the spawn and still owns it.
	SourceThreadID string
	// SourceChatID / SourceChatTitle identify the conversation that owns it,
	// so a refusal can tell the model where the agent actually lives.
	SourceChatID    string
	SourceChatTitle string
}

// maxBranchAncestry bounds the walk up a chain of branches-of-branches. Each
// hop is a user action, so real chains are a handful deep; the bound exists
// only so corrupt parent links cannot loop or run away.
const maxBranchAncestry = 32

// ListInheritedSpawnChildren returns the sub-agents threadID inherited by
// being a branch (or a branch of a branch) of the conversations that spawned
// them — every spawn an ancestor issued at or before the point the branch was
// taken — nearest ancestor first.
//
// Only chat-crossing forks count. A spawned agent running in fork thread mode
// is also an origin=fork thread, with the spawner as its parent, and it must
// not mistake its siblings for children of its own; what makes a fork a
// BRANCH is that it crossed into another chat, the same test ListBranches
// applies.
//
// A thread that is not a branch returns nil: the common case costs one
// primary-key read.
func (r *Repo) ListInheritedSpawnChildren(ctx context.Context, threadID string) ([]*InheritedSpawnChild, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}

	var inherited []*InheritedSpawnChild
	current := threadID
	for hop := 0; hop < maxBranchAncestry; hop++ {
		thread, parentChatID, err := r.GetThreadWithParent(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("failed to load thread %s: %w", current, err)
		}
		isBranch := thread.Origin == ThreadOriginFork &&
			thread.ParentThreadID != nil && *thread.ParentThreadID != current &&
			parentChatID != nil && *parentChatID != thread.ChatID
		if !isBranch || thread.ForkAtMessageID == nil {
			// Not a branch, or a branch that inherited nothing (its parent
			// had no messages yet) — and so nothing from further up either.
			return inherited, nil
		}

		forkPoint, err := r.GetMessage(ctx, *thread.ForkAtMessageID)
		if err != nil {
			return nil, fmt.Errorf("failed to load fork point %s of thread %s: %w", *thread.ForkAtMessageID, current, err)
		}
		sourceThreadID := *thread.ParentThreadID
		children, err := r.ListSpawnChildren(ctx, sourceThreadID)
		if err != nil {
			return nil, err
		}
		// The title only makes a refusal friendlier; a failed read must not
		// cost the caller the listing.
		sourceChatTitle := ""
		if chat, chatErr := r.GetChat(ctx, *parentChatID); chatErr == nil && chat != nil {
			sourceChatTitle = chat.Title
		}
		for _, child := range children {
			if !spawnedAtOrBefore(child, forkPoint) {
				continue
			}
			inherited = append(inherited, &InheritedSpawnChild{
				SpawnChild:      *child,
				SourceThreadID:  sourceThreadID,
				SourceChatID:    *parentChatID,
				SourceChatTitle: sourceChatTitle,
			})
		}
		// The next hop's cutoff is the parent's OWN fork point: the branch
		// sees everything its parent inherited, which is all of the
		// grandparent up to where the parent was branched.
		current = sourceThreadID
	}
	return inherited, nil
}

// spawnedAtOrBefore reports whether child's spawn call is part of the history
// up to and including forkPoint, i.e. in the branch's inherited transcript.
func spawnedAtOrBefore(child *SpawnChild, forkPoint *Message) bool {
	if child.IssuingMessageOrdinal != nil {
		return *child.IssuingMessageOrdinal <= forkPoint.Ordinal
	}
	// No issuing message on record: fall back to time, which is coarser but
	// errs the same way the transcript does — a call made before the fork
	// point was written is one the branch can see.
	return !child.RequestedAt.After(forkPoint.CreatedAt)
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
		if row.ChildWorkflowID.Valid {
			s := row.ChildWorkflowID.String
			child.ChildWorkflowID = &s
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
		if row.IssuingMessageOrdinal.Valid {
			ordinal := row.IssuingMessageOrdinal.Int64
			child.IssuingMessageOrdinal = &ordinal
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

// StoppedSpawnAgent is a sub-agent that stopped on a failure — its step's
// retries exhausted, a provider error, a usage limit — and has not run since:
// everything resuming it needs, read from durable rows. See
// GetStoppedSpawnAgent.
//
// JSON-tagged because it is also an activity's result
// (LookupStoppedSubAgent), which the workflow decodes.
type StoppedSpawnAgent struct {
	// ToolCallID is the spawn call that started the agent's latest run.
	ToolCallID string `json:"tool_call_id"`
	// ParentThreadID is the thread that issued that call: the recipient of
	// the agent's reports.
	ParentThreadID string `json:"parent_thread_id"`
	// ToolInput is the spawn call's input as the LLM sent it (preset, title).
	ToolInput       string `json:"tool_input,omitempty"`
	ChildThreadID   string `json:"child_thread_id"`
	ChildWorkflowID string `json:"child_workflow_id"`
	// IssuingWorkflowID is the workflow that issued the spawn: the chat's
	// root for a top-level agent, the spawning agent's row for a nested one.
	IssuingWorkflowID string `json:"issuing_workflow_id"`
	// SpawnDepth is 1 for an agent the root spawned, 2 for one a top-level
	// agent spawned, and so on.
	SpawnDepth int `json:"spawn_depth"`
	// WorktreePath is the checkout of the thread's own worktree, when it has
	// one: where the agent works instead of the chat's checkout.
	WorktreePath string `json:"worktree_path,omitempty"`
	// Resumes is the highest resume slot its resumed runs have reported
	// under (ToolCallID#resume-N), 0 when none has: the next resume reports
	// past it.
	Resumes int `json:"resumes,omitempty"`
}

// resumeSlotSeparator joins a spawn's tool call id to the number of a resumed
// run's report slot. runtime.resumeStoppedSpawn writes the slots; this reads
// them back.
const resumeSlotSeparator = "#resume-"

// GetStoppedSpawnAgent returns the sub-agent on threadID if it stopped on a
// failure, and nil, nil if it did not: no such sub-agent in chatID, or one
// that is running, completed, or was stopped by the user.
//
// "Stopped on a failure" is the agent's thread AND its latest spawn run both
// recorded failed. That is the durable form of what the workflow keeps in
// memory as a stopped spawn (runtime.ChildWorkflowTracker.stoppedSpawns), so a
// message can resume the agent from any execution of the chat — a successor
// after the run that saw it stop has completed, a fresh restart of a run that
// died, or a chat whose agent stopped before the run kept such a record at
// all.
func (r *Repo) GetStoppedSpawnAgent(ctx context.Context, chatID, threadID string) (*StoppedSpawnAgent, error) {
	if chatID == "" || threadID == "" {
		return nil, fmt.Errorf("chat ID and thread ID are required")
	}
	if r.DB == nil {
		return nil, fmt.Errorf("repository has no database connection")
	}
	q := pgdb.New(r.DB.DB(ctx))
	row, err := q.GetLatestSpawnRunForThread(ctx, pgdb.GetLatestSpawnRunForThreadParams{
		ChatID:   chatID,
		ThreadID: threadID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the latest spawn run of thread %s: %w", threadID, err)
	}
	stopped := row.ThreadOrigin == string(ThreadOriginSpawn) &&
		row.ThreadStatus == ThreadStatusFailed &&
		WorkflowState(row.WorkflowState) == WorkflowStateStopped &&
		WorkflowStopReason(row.WorkflowStopReason) == StopReasonFailed
	if !stopped || row.ParentThreadID == "" {
		return nil, nil
	}

	slots, err := q.ListSpawnResumeReportIDs(ctx, pgdb.ListSpawnResumeReportIDsParams{
		ChatID:     chatID,
		SlotPrefix: row.ToolCallID + resumeSlotSeparator,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list the resume reports of spawn %s: %w", row.ToolCallID, err)
	}
	resumes := 0
	for _, slot := range slots {
		n, convErr := strconv.Atoi(strings.TrimPrefix(slot, row.ToolCallID+resumeSlotSeparator))
		if convErr == nil && n > resumes {
			resumes = n
		}
	}

	return &StoppedSpawnAgent{
		ToolCallID:        row.ToolCallID,
		ParentThreadID:    row.ParentThreadID,
		ToolInput:         string(row.ToolInput),
		ChildThreadID:     threadID,
		ChildWorkflowID:   row.ChildWorkflowID,
		IssuingWorkflowID: row.IssuingWorkflowID,
		SpawnDepth:        int(row.SpawnDepth),
		WorktreePath:      row.WorktreePath,
		Resumes:           resumes,
	}, nil
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
