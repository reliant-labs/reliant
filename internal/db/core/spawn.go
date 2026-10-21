// Copyright (c) 2025 Reliant Labs
package core

import "time"

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

// ToolGrant is what one recorded tool result granted its thread. See
// db.Repo.ListToolGrants.
type ToolGrant struct {
	ThreadID string
	Tools    []string
}

// SpawnRun is the latest spawn run of the sub-agent on one thread: the spawn
// tool call that started it, joined to that run's workflow row, the thread,
// and the thread's own worktree. db.Repo.GetStoppedSpawnAgent decides from it
// whether the agent stopped on a failure.
type SpawnRun struct {
	ToolCallID string
	// ParentThreadID is the thread that issued the call ("" when unknown).
	ParentThreadID    string
	ToolInput         []byte
	ChildWorkflowID   string
	IssuingWorkflowID string
	WorkflowStatus    WorkflowStatus
	ThreadStatus      ThreadStatus
	ThreadOrigin      ThreadOrigin
	WorktreePath      string
	// SpawnDepth counts the run's ancestors up its workflow tree: 1 for a
	// spawn the root issued.
	SpawnDepth int
}
