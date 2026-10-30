// Copyright (c) 2025 Reliant Labs
package db

import (
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	core "github.com/reliant-labs/reliant/internal/db/core"
)

// ChatState represents the notification/lifecycle state of a chat.
type ChatState = core.ChatState

const (
	// ChatStateUnspecified is the zero value.
	ChatStateUnspecified ChatState = core.ChatStateUnspecified
	// ChatStateIdle means user has viewed/acknowledged, no pending notifications.
	ChatStateIdle ChatState = core.ChatStateIdle
	// ChatStateArchived means chat is archived.
	ChatStateArchived ChatState = core.ChatStateArchived
)

// ChatOrigin is who started a chat; see core.ChatOrigin.
type ChatOrigin = core.ChatOrigin

// ChatOriginUser is a conversation a person started; see core.ChatOriginUser.
const ChatOriginUser = core.ChatOriginUser

// ProviderOAuthMarker is the api_keys value an OAuth provider stores when it
// connects; never a credential. See core.ProviderOAuthMarker.
const ProviderOAuthMarker = core.ProviderOAuthMarker

// Run-list types.
type (
	RunListFilters = core.RunListFilters
	RunListItem    = core.RunListItem
	RunCursor      = core.RunCursor

	UnlaunchedTriggerEvent = core.UnlaunchedTriggerEvent
	RunDisplayState        = core.RunDisplayState
)

const (
	RunDisplayQueued            = core.RunDisplayQueued
	RunDisplayRunning           = core.RunDisplayRunning
	RunDisplayNeedsInput        = core.RunDisplayNeedsInput
	RunDisplayPaused            = core.RunDisplayPaused
	RunDisplayCompleted         = core.RunDisplayCompleted
	RunDisplayFailed            = core.RunDisplayFailed
	RunDisplayCancelled         = core.RunDisplayCancelled
	RunDisplayWaitingForMachine = core.RunDisplayWaitingForMachine
)

// Chat represents a top-level conversation.
type Chat = core.Chat

// ChatRow is core.ChatRow.
type ChatRow = core.ChatRow

type ChatActivityState = core.ChatActivityState

// ArchivedChatInfo represents an archived chat with worktree information.
type ArchivedChatInfo = core.ArchivedChatInfo

// QueuedForMachineChat, QueuedMachine, PausedForMachineChat and
// WaitingForMachineRun: see core.
type (
	QueuedForMachineChat = core.QueuedForMachineChat
	QueuedMachine        = core.QueuedMachine
	PausedForMachineChat = core.PausedForMachineChat
	WaitingForMachineRun = core.WaitingForMachineRun
)

// Message is an alias to the shared core message model.
type Message = core.Message

// MessageContentBlock is an alias to the shared core content block model.
type MessageContentBlock = core.MessageContentBlock

// Project is an alias to the shared core project model.
type Project = core.Project

// ProjectDaemon is an alias to the shared core project-daemon installation
// model. Tracks per-daemon clones of a Project.
type ProjectDaemon = core.ProjectDaemon

// ProjectConfigRecord is an alias to the shared core project config record.
type ProjectConfigRecord = core.ProjectConfigRecord

// CleanupMetadata is an alias to the shared core cleanup metadata model.
type CleanupMetadata = core.CleanupMetadata

// Worktree is an alias to the shared core worktree model.
type Worktree = core.Worktree

// Plan is an alias to the shared core plan model.
type Plan = core.Plan

// PlanStatus represents the status of a plan.
type PlanStatus = reliantv1.PlanStatus

const (
	PlanStatusPending   PlanStatus = reliantv1.PlanStatus_PLAN_STATUS_PENDING
	PlanStatusActive    PlanStatus = reliantv1.PlanStatus_PLAN_STATUS_IN_PROGRESS
	PlanStatusCompleted PlanStatus = reliantv1.PlanStatus_PLAN_STATUS_COMPLETED
	PlanStatusCancelled PlanStatus = reliantv1.PlanStatus_PLAN_STATUS_CANCELLED
)

// Task is an alias to the shared core task model.
type Task = core.Task

// TaskDependency is an alias to the shared core task dependency model.
type TaskDependency = core.TaskDependency

// TaskStatus represents the status of a task.
type TaskStatus = reliantv1.TaskStatus

const (
	TaskStatusPending    TaskStatus = reliantv1.TaskStatus_TASK_STATUS_PENDING
	TaskStatusInProgress TaskStatus = reliantv1.TaskStatus_TASK_STATUS_IN_PROGRESS
	TaskStatusCompleted  TaskStatus = reliantv1.TaskStatus_TASK_STATUS_COMPLETED
	TaskStatusFailed     TaskStatus = reliantv1.TaskStatus_TASK_STATUS_FAILED
	TaskStatusBlocked    TaskStatus = reliantv1.TaskStatus_TASK_STATUS_BLOCKED
	TaskStatusCancelled  TaskStatus = reliantv1.TaskStatus_TASK_STATUS_CANCELLED
	TaskStatusSkipped    TaskStatus = reliantv1.TaskStatus_TASK_STATUS_SKIPPED
)

// TaskStats represents statistics about tasks in a plan
type TaskStats struct {
	Total      int
	Pending    int
	InProgress int
	Completed  int
	Failed     int
	Blocked    int
	Cancelled  int
	Skipped    int
}

// ToolCall is an alias to the shared core tool call model.
type ToolCall = core.ToolCall

// ToolCallResult is an alias to the shared core tool call result model.
type ToolCallResult = core.ToolCallResult

// StrandedBackgroundSpawn is an alias to the shared core model.
type StrandedBackgroundSpawn = core.StrandedBackgroundSpawn

// BackgroundedProcessCall is an alias to the shared core model.
type BackgroundedProcessCall = core.BackgroundedProcessCall

// ReapedThread is an alias to the shared core model.
type ReapedThread = core.ReapedThread

// ReapedWorkflow is an alias to the shared core model.
type ReapedWorkflow = core.ReapedWorkflow

// The durable tool call status enum is deliberately NOT re-exported here as
// `db.ToolCallStatus`: that name is already taken by the string-valued status
// on ToolCallUpdate (chat_update_types.go), which describes the transient
// streaming event rather than the persisted row. Two same-named types with
// different underlying kinds in one package is a trap, so the durable one
// stays qualified at its use sites — core.ToolCallStatusCompleted, etc.

// Setting is an alias to the shared core setting model.
type Setting = core.Setting

// VisibilityOverride is an alias to the shared core visibility override model.
type VisibilityOverride = core.VisibilityOverride

// ItemDefault is an alias to the shared core item default model.
type ItemDefault = core.ItemDefault

// Approval is an alias to the shared core approval model.
type Approval = core.Approval

// AgentMessage is an alias to the shared core agent mailbox model.
type AgentMessage = core.AgentMessage

// SpawnReportOutcome aliases core.SpawnReportOutcome.
type SpawnReportOutcome = core.SpawnReportOutcome

const (
	SpawnReportInserted        = core.SpawnReportInserted
	SpawnReportSuperseded      = core.SpawnReportSuperseded
	SpawnReportAlreadyReported = core.SpawnReportAlreadyReported
)

// Daemon, its lifecycle update and its attachment lease are core's
// (internal/store maps the daemons and daemon_attachment rows onto them).
type (
	Daemon                 = core.Daemon
	DaemonLifecycleUpdate  = core.DaemonLifecycleUpdate
	DaemonAttachmentSource = core.DaemonAttachmentSource
	DaemonAttachment       = core.DaemonAttachment
	DaemonBuild            = core.DaemonBuild
)

const (
	DaemonAttachmentSourceInbound  = core.DaemonAttachmentSourceInbound
	DaemonAttachmentSourceOutbound = core.DaemonAttachmentSourceOutbound
)

// DiscoveredProject represents a daemon-reported project on disk.
// This is used for daemon registry scaffolding serialization helpers.
type DiscoveredProject struct {
	Path      string `json:"path"`
	Name      string `json:"name,omitempty"`
	IsGitRepo bool   `json:"is_git_repo,omitempty"`
}

// ToolExecutionStatus represents the status of a tool execution.
type ToolExecutionStatus = reliantv1.ToolExecutionStatus

const (
	ToolExecStatusPending   ToolExecutionStatus = reliantv1.ToolExecutionStatus_TOOL_EXECUTION_STATUS_PENDING
	ToolExecStatusExecuting ToolExecutionStatus = reliantv1.ToolExecutionStatus_TOOL_EXECUTION_STATUS_EXECUTING
	ToolExecStatusCompleted ToolExecutionStatus = reliantv1.ToolExecutionStatus_TOOL_EXECUTION_STATUS_COMPLETED
	ToolExecStatusFailed    ToolExecutionStatus = reliantv1.ToolExecutionStatus_TOOL_EXECUTION_STATUS_FAILED
	ToolExecStatusCancelled ToolExecutionStatus = reliantv1.ToolExecutionStatus_TOOL_EXECUTION_STATUS_CANCELLED
	ToolExecStatusTimeout   ToolExecutionStatus = reliantv1.ToolExecutionStatus_TOOL_EXECUTION_STATUS_TIMEOUT
)

// StreamingStateInfo contains computed streaming state information
type StreamingStateInfo struct {
	State       string     // "streaming", "complete", "failed"
	StartedAt   *time.Time // When streaming started (first block created)
	CompletedAt *time.Time // When streaming completed (all blocks valid)
	IsComplete  bool       // Convenience flag
	IsStreaming bool       // Convenience flag
}

// ComputeStreamingState derives streaming state from message content blocks
// This is the new single source of truth - no mutable state fields needed
//
// Algorithm: Single pass through blocks for efficiency
// - Track first/last timestamps, validity, and orphaned tool calls in one pass
// - Early exit on invalid blocks
func ComputeStreamingState(blocks []MessageContentBlock) StreamingStateInfo {
	if len(blocks) == 0 {
		// No blocks means message creation started but never completed streaming
		return StreamingStateInfo{
			State:       "streaming",
			IsStreaming: true,
		}
	}

	// Single pass: track timestamps and validity
	var firstCreatedAt *time.Time
	var lastCreatedAt *time.Time

	for i := range blocks {
		block := &blocks[i]

		// Track first and last timestamps
		if firstCreatedAt == nil || block.CreatedAt.Before(*firstCreatedAt) {
			firstCreatedAt = &block.CreatedAt
		}
		if lastCreatedAt == nil || block.CreatedAt.After(*lastCreatedAt) {
			lastCreatedAt = &block.CreatedAt
		}

		// Early exit: if ANY block is invalid, streaming is incomplete
		if !IsBlockValid(block) {
			return StreamingStateInfo{
				State:       "streaming",
				StartedAt:   firstCreatedAt,
				IsStreaming: true,
			}
		}
	}

	// All blocks valid - message streaming is complete
	// Note: Whether tool execution has occurred is a separate concern checked at conversation level
	// Tool results are stored in separate TOOL role messages, not in the assistant message
	return StreamingStateInfo{
		State:       "complete",
		StartedAt:   firstCreatedAt,
		CompletedAt: lastCreatedAt,
		IsComplete:  true,
	}
}

// IsBlockValid checks if a content block has all required fields
func IsBlockValid(block *MessageContentBlock) bool {
	switch block.BlockType {
	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT:
		return block.Content != nil && *block.Content != ""

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL:
		return block.ToolCallID != nil && *block.ToolCallID != ""

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT:
		return block.ToolCallID != nil && *block.ToolCallID != "" && block.Content != nil

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING:
		// A signature with no readable text is a VALID thinking block. The
		// provider signs reasoning it does not always return in text form, and
		// the signature is what lets the next turn replay the block and
		// continue — requiring Content here would filter out exactly the rows
		// that keep a thread moving.
		if block.Content != nil && *block.Content != "" {
			return true
		}
		return block.ThoughtSignature != nil && *block.ThoughtSignature != ""

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING:
		// The sealed payload lives in Content and is the whole block.
		return block.Content != nil && *block.Content != ""

	case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE,
		reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE:
		return block.Content != nil && *block.Content != ""

	default:
		// Unknown block type - consider invalid
		return false
	}
}

// ChatUpdate represents an update in the chat_updates table.
type ChatUpdate = core.ChatUpdate

// UserUpdateType represents the type of a user update (proto enum integer).
type UserUpdateType = reliantv1.UserUpdateType

const (
	// Chat updates
	UserUpdateChatStateChange     UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_STATE_CHANGE
	UserUpdateChatConfigChanged   UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_CONFIG_CHANGED
	UserUpdateChatCreated         UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_CREATED
	UserUpdateChatTitleChanged    UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_TITLE_CHANGED
	UserUpdateChatDeleted         UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_DELETED
	UserUpdateChatActivityChanged UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_ACTIVITY_CHANGED // Workflow started/completed/failed/cancelled

	// Project updates
	UserUpdateProjectCreated         UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_PROJECT_CREATED
	UserUpdateProjectDeleted         UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_PROJECT_DELETED
	UserUpdateProjectSettingsChanged UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_PROJECT_SETTINGS_CHANGED

	// Worktree updates
	UserUpdateWorktreeCreated       UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_WORKTREE_CREATED
	UserUpdateWorktreeDeleted       UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_WORKTREE_DELETED
	UserUpdateWorktreeStatusChanged UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_WORKTREE_STATUS_CHANGED

	// Background process updates
	UserUpdateProcessStarted     UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_PROCESS_STARTED
	UserUpdateProcessOutput      UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_PROCESS_OUTPUT
	UserUpdateProcessCompleted   UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_PROCESS_COMPLETED
	UserUpdateProcessFailed      UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_PROCESS_FAILED
	UserUpdateProcessPortChanged UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_PROCESS_PORT_CHANGED

	// General notification
	UserUpdateNotification UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_NOTIFICATION

	// Refetch signals - tell frontend to re-fetch specific data
	UserUpdateRefetch UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_REFETCH

	// Daemon heartbeat - ephemeral signal so frontend knows daemon is alive
	UserUpdateDaemonHeartbeat UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_DAEMON_HEARTBEAT

	// Workflow draft written (by any chat, tool or RPC) - open editors refetch
	UserUpdateWorkflowDraftUpdated UserUpdateType = reliantv1.UserUpdateType_USER_UPDATE_TYPE_WORKFLOW_DRAFT_UPDATED
)

// UserUpdateEntityType represents the type of entity a user update is about (proto enum integer).
type UserUpdateEntityType = reliantv1.EntityType

const (
	EntityTypeChat              UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_CHAT
	EntityTypeProject           UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_PROJECT
	EntityTypeWorktree          UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_WORKTREE
	EntityTypeBackgroundProcess UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_BACKGROUND_PROCESS
	EntityTypeSystem            UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_SYSTEM
	EntityTypeWorkflowDraft     UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_WORKFLOW_DRAFT
)

// UserUpdate represents an update in the user_updates table: a
// workspace-level update pushed via the global WebSocket.
type UserUpdate = core.UserUpdate

// BackgroundProcessStatus represents the status of a background process.
type BackgroundProcessStatus = reliantv1.BackgroundProcessStatus

const (
	BgProcessStatusRunning          BackgroundProcessStatus = reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_RUNNING
	BgProcessStatusCompleted        BackgroundProcessStatus = reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_COMPLETED
	BgProcessStatusFailed           BackgroundProcessStatus = reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_FAILED
	BgProcessStatusKilled           BackgroundProcessStatus = reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_KILLED
	BgProcessStatusKilledExternally BackgroundProcessStatus = reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_KILLED_EXTERNALLY
	BgProcessStatusStale            BackgroundProcessStatus = reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_STALE
)

// BgProcessStatusToString converts a BackgroundProcessStatus enum to its lowercase string name.
// This is needed for the shell BackgroundManager which uses string statuses in-memory.
func BgProcessStatusToString(s BackgroundProcessStatus) string {
	switch s {
	case BgProcessStatusRunning:
		return "running"
	case BgProcessStatusCompleted:
		return "completed"
	case BgProcessStatusFailed:
		return "failed"
	case BgProcessStatusKilled:
		return "killed"
	case BgProcessStatusKilledExternally:
		return "killed_externally"
	case BgProcessStatusStale:
		return "stale"
	default:
		return "unknown"
	}
}

// The background process record and its output lines are core's
// (internal/store maps the background_process* rows onto them).
type (
	BackgroundProcessSourceType = core.BackgroundProcessSourceType
	BackgroundProcessOutputLine = core.BackgroundProcessOutputLine
	BackgroundProcess           = core.BackgroundProcess
)

const (
	BgProcessSourceLLM            = core.BgProcessSourceLLM
	BgProcessSourcePackageCommand = core.BgProcessSourcePackageCommand
	BgProcessSourceManual         = core.BgProcessSourceManual
)

// WorkflowStatus is a run's lifecycle: state plus the reason it stopped.
// Build one with the core constructors (db.Pending(), db.Active(),
// db.Completed(), db.Failed(), db.Paused(), db.Cancelled()) and ask it
// questions with Live() / Resumable() rather than comparing fields.
type (
	WorkflowStatus     = core.WorkflowStatus
	WorkflowState      = core.WorkflowState
	WorkflowStopReason = core.WorkflowStopReason
)

// Lifecycle constructors, re-exported so the db package reads naturally at
// call sites that already import it.
var (
	Pending   = core.Pending
	Active    = core.Active
	Completed = core.Completed
	Failed    = core.Failed
	Paused    = core.Paused
	Cancelled = core.Cancelled
	Stopped   = core.Stopped
)

const (
	WorkflowStatePending = core.WorkflowStatePending
	WorkflowStateActive  = core.WorkflowStateActive
	WorkflowStateStopped = core.WorkflowStateStopped

	StopReasonUnspecified = core.StopReasonUnspecified
	StopReasonCompleted   = core.StopReasonCompleted
	StopReasonFailed      = core.StopReasonFailed
	StopReasonPaused      = core.StopReasonPaused
	StopReasonCancelled   = core.StopReasonCancelled
)

// Workflow is an alias to the shared core workflow model.
type Workflow = core.Workflow

// Thread is an alias to the shared core thread model.
type Thread = core.Thread

// ThreadOrigin is an alias to the shared core thread-origin type.
type ThreadOrigin = core.ThreadOrigin

// Thread origins — how a thread came to exist.
const (
	ThreadOriginMain  = core.ThreadOriginMain
	ThreadOriginSpawn = core.ThreadOriginSpawn
	ThreadOriginFork  = core.ThreadOriginFork
	ThreadOriginNode  = core.ThreadOriginNode
)

// ThreadStatusForStopReason maps a workflow's stop reason onto the thread
// status its owned threads take. See core for why PAUSED is not terminal.
var ThreadStatusForStopReason = core.ThreadStatusForStopReason

// ThreadStatus is an alias to the shared core thread lifecycle status.
type ThreadStatus = core.ThreadStatus

// Thread lifecycle statuses, mirroring the workflow lifecycle.
const (
	ThreadStatusRunning   = core.ThreadStatusRunning
	ThreadStatusCompleted = core.ThreadStatusCompleted
	ThreadStatusFailed    = core.ThreadStatusFailed
	ThreadStatusCancelled = core.ThreadStatusCancelled
)

// ContextWindow is an alias to the shared core context-window model.
type ContextWindow = core.ContextWindow

// WorkflowDraft is an alias to the shared core workflow draft model.
type WorkflowDraft = core.WorkflowDraft

// WorkflowDraftStatus is a stored workflow's lifecycle (draft / complete).
type WorkflowDraftStatus = core.WorkflowDraftStatus

// WorkflowDraftNotRunnableError reports that a slug names a draft.
type WorkflowDraftNotRunnableError = core.WorkflowDraftNotRunnableError

const (
	WorkflowDraftStatusDraft    = core.WorkflowDraftStatusDraft
	WorkflowDraftStatusComplete = core.WorkflowDraftStatusComplete
)

// WorkflowScenario is an alias to the shared core workflow scenario model.
type WorkflowScenario = core.WorkflowScenario

// StepExecution is an alias to the shared core step-execution model.
type StepExecution = core.StepExecution

// ChatStepExecution is an alias to the shared core chat-scoped step-execution
// model — every scalar column of a step, without OutputJSON.
type ChatStepExecution = core.ChatStepExecution

// WorkflowCheckpoint is an alias to the shared core workflow-checkpoint model
// (position truth for resume-at-position).
type WorkflowCheckpoint = core.WorkflowCheckpoint

// ============================================================================
// Node Execution State Types
// ============================================================================
// These types support real-time streaming of workflow execution state for UI visualization

// NodeExecutionStatus represents the lifecycle state of a workflow node.
type NodeExecutionStatus = reliantv1.NodeExecutionStatus

const (
	NodeStatusPending   NodeExecutionStatus = reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_PENDING
	NodeStatusRunning   NodeExecutionStatus = reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_RUNNING
	NodeStatusCompleted NodeExecutionStatus = reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_COMPLETED
	NodeStatusFailed    NodeExecutionStatus = reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_FAILED
	NodeStatusCancelled NodeExecutionStatus = reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_CANCELLED
	NodeStatusSkipped   NodeExecutionStatus = reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_SKIPPED
)

// NodeExecutionState represents the current execution state of a workflow node
// This is used for streaming node status to the UI
type NodeExecutionState struct {
	NodeID        string              `json:"node_id"`                  // Step/node identifier from workflow definition
	NodeType      string              `json:"node_type"`                // "agent", "run", "action", "approval", "loop", "join", "workflow"
	Status        NodeExecutionStatus `json:"status"`                   // Current execution status
	WorkflowID    string              `json:"workflow_id"`              // Parent workflow UUID
	ChatID        string              `json:"chat_id"`                  // Associated chat for routing
	ParentNodeID  *string             `json:"parent_node_id,omitempty"` // Parent node for nested workflows/loops
	ActivityID    *string             `json:"activity_id,omitempty"`    // Temporal activity ID for correlation
	StartedAt     *time.Time          `json:"started_at,omitempty"`     // When execution started
	CompletedAt   *time.Time          `json:"completed_at,omitempty"`   // When execution completed
	DurationMs    *int64              `json:"duration_ms,omitempty"`    // Execution duration in milliseconds
	ExitCode      *int                `json:"exit_code,omitempty"`      // Exit code for run steps
	ErrorMessage  *string             `json:"error_message,omitempty"`  // Error message if failed
	Iteration     *int                `json:"iteration,omitempty"`      // Current iteration for loop nodes
	MaxIterations *int                `json:"max_iterations,omitempty"` // Max iterations for loop nodes
	Metadata      map[string]string   `json:"metadata,omitempty"`       // Additional node-specific metadata
}

// ============================================================================
// Preset Types
// ============================================================================

// Preset is an alias to the shared core preset model.
type Preset = core.Preset

// ============================================================================
// Trigger Types
// ============================================================================

// Trigger is an alias to the shared core trigger model — a standing
// instruction to start a run without a human typing.
type Trigger = core.Trigger

// TriggerEvent is an alias to the shared core trigger-event model — one
// firing, of a stored trigger or of an ad hoc source such as a chat start.
type TriggerEvent = core.TriggerEvent

// TriggerFilters contains options for filtering triggers.
type TriggerFilters = core.TriggerFilters

// ============================================================================
// Question Types
// ============================================================================

// QuestionStatus represents the status of a question (stored as INTEGER in DB)
const (
	QuestionStatusPending  = core.QuestionStatusPending
	QuestionStatusResolved = core.QuestionStatusResolved
)

// Question is an alias to the shared core question model.
type Question = core.Question
