// Copyright (c) 2025 Reliant Labs
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
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

// StoppedSpawnRecord is a failed latest spawn plus its highest prior report
// slot, read by a single set-based query for whole-chat resume.
type StoppedSpawnRecord struct {
	ToolCallID        string
	ParentThreadID    string
	ToolInput         string
	ChildThreadID     string
	ChildWorkflowID   string
	IssuingWorkflowID string
	SpawnDepth        int
	WorktreePath      string
	Resumes           int
	// LastFailure is the body of the latest real failure report the spawn
	// sent its parent, "" when none.
	LastFailure string
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

// StoppedSpawnAgent is a sub-agent that stopped on a failure — its step's
// retries exhausted, a provider error, a usage limit — and has not run since:
// everything resuming it needs, read from durable rows. See
// store.GetStoppedSpawnAgent.
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
	// LastFailure is the FailureReportDigest of the latest failure report the
	// agent's parent received from it (any of its report slots), "" when
	// there is none: a resumed run that fails with the same report does not
	// send it again (runtime.SpawnHandoff.LastFailure).
	LastFailure string `json:"last_failure,omitempty"`
}

// FailureReportDigest identifies a sub-agent's failure report by its body,
// so a repeat of the same failure can be told from a new one without carrying
// the report itself through workflow state. Empty for an empty body.
func FailureReportDigest(body string) string {
	if body == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:16])
}
