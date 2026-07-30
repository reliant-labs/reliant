// Copyright (c) 2025 Reliant Labs
package db

import (
	"encoding/json"
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

// Chat represents a top-level conversation.
type Chat = core.Chat

// ArchivedChatInfo represents an archived chat with worktree information.
type ArchivedChatInfo = core.ArchivedChatInfo

// Message is an alias to the shared core message model.
type Message = core.Message

// MessageContentBlock is an alias to the shared core content block model.
type MessageContentBlock = core.MessageContentBlock

// Project is an alias to the shared core project model.
type Project = core.Project

// ProjectDaemon is an alias to the shared core project-daemon installation
// model. Tracks per-daemon clones of a Project.
type ProjectDaemon = core.ProjectDaemon

// ProjectConfigRecord stores daemon-pushed configuration for a project.
// This table is part of cloud-refactor scaffolding and may not exist in all runtimes yet.
type ProjectConfigRecord struct {
	ID                   string
	ProjectID            string
	DaemonID             string
	UserConfigYAML       *string
	ProjectConfigYAML    *string
	LocalConfigYAML      *string
	GlobalMemoryMD       *string
	ProjectMemoryMD      *string
	MCPConfigs           *string
	ProjectWorkflowsJSON *string
	ProjectPresetsJSON   *string
	ProjectScenariosJSON *string
	ProjectSkillsJSON    *string
	RepoMemoriesJSON     *string
	// RuntimeType is the serving daemon's runtime/sandbox type ("kata",
	// "gvisor"); nil for local/unknown daemons. Captured from the pushing
	// daemon's registration label at config-snapshot persist time.
	RuntimeType *string
	PushedAt    time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

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

// Setting is an alias to the shared core setting model.
type Setting = core.Setting

// CodexAuthTokens is an alias to the shared core Codex OAuth token model.
type CodexAuthTokens = core.CodexAuthTokens

// CopilotAuthTokens is an alias to the shared core Copilot OAuth token model.
type CopilotAuthTokens = core.CopilotAuthTokens

// ClaudeAuthTokens is an alias to the shared core Claude OAuth token model.
type ClaudeAuthTokens = core.ClaudeAuthTokens

// VisibilityOverride is an alias to the shared core visibility override model.
type VisibilityOverride = core.VisibilityOverride

// ItemDefault is an alias to the shared core item default model.
type ItemDefault = core.ItemDefault

// Approval is an alias to the shared core approval model.
type Approval = core.Approval

// Daemon represents a persisted tools daemon identity registration.
// Lifecycle state (connection, heartbeat) lives in daemon_attachment.
// Capabilities and ProjectPaths are JSON-encoded arrays.
type Daemon struct {
	ID           string
	UserID       string
	Hostname     *string
	Platform     *string
	Capabilities *string
	ProjectPaths *string
	// DaemonType is the type of daemon: "managed" (cloud-hosted) or
	// "self_hosted" (user-run local daemon). Empty/nil if undetermined.
	DaemonType *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type DaemonAttachmentSource string

const (
	DaemonAttachmentSourceInbound  DaemonAttachmentSource = "inbound"
	DaemonAttachmentSourceOutbound DaemonAttachmentSource = "outbound"
)

type DaemonAttachment struct {
	DaemonID           string
	UserID             string
	Source             DaemonAttachmentSource
	PodIP              *string
	PodPort            *int
	AttachedAt         time.Time
	LastStreamActivity time.Time
	// Workspace memory telemetry from the daemon heartbeat (cloud daemons in
	// a cgroup-limited pod). MemoryLimitBytes == 0 means "not reported"
	// (local daemons without cgroup accounting). MemoryPressure is the
	// daemon's hysteresis-smoothed pressure bit (asserts >= 85% of the
	// limit, clears below 75%).
	MemoryUsedBytes  int64
	MemoryLimitBytes int64
	MemoryPressure   bool
	// DetectedPorts are the loopback/wildcard LISTEN ports the daemon
	// reported in its heartbeat (what the in-pod preview forwarder can
	// reach). JSON-encoded in the detected_ports column. Nil/empty when
	// nothing is listening or the daemon doesn't report (local daemons).
	DetectedPorts []uint32
}

// DaemonPAT kinds. One table (daemon_pats) and one token format (rlnt_pat_)
// back every personal access token; the kind discriminates what a token may
// authenticate. Kind separation is enforced at validation time: the gRPC auth
// interceptor accepts kind='api' only, the gateway PAT validator accepts
// kind='daemon' only.
const (
	// DaemonPATKindDaemon authenticates daemon <-> gateway streams.
	DaemonPATKindDaemon = "daemon"
	// DaemonPATKindAPI authenticates regular user API requests through the
	// same middleware path as JWTs.
	DaemonPATKindAPI = "api"
)

// DaemonPAT is a personal access token (rlnt_pat_ format). Despite the legacy
// name, the table holds every PAT kind — see DaemonPATKind* above.
type DaemonPAT struct {
	ID          string
	UserID      string
	UserEmail   string // Email captured at mint time (api kind) so PAT auth resolves the same claims JWTs do
	DaemonID    string // Bound daemon ID (empty for unbound/general-purpose PATs)
	Kind        string // DaemonPATKindDaemon (default) or DaemonPATKindAPI
	TokenHash   string // SHA-256 hex digest of the raw token
	TokenPrefix string // First 8 chars of raw token for display in UI
	Name        string // Human-readable label ("Sean's MacBook", "CI runner")
	Ephemeral   bool   // Auto-created tokens (desktop session) — revoked on shutdown
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
}

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
)

// UserUpdateEntityType represents the type of entity a user update is about (proto enum integer).
type UserUpdateEntityType = reliantv1.EntityType

const (
	EntityTypeChat              UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_CHAT
	EntityTypeProject           UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_PROJECT
	EntityTypeWorktree          UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_WORKTREE
	EntityTypeBackgroundProcess UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_BACKGROUND_PROCESS
	EntityTypeSystem            UserUpdateEntityType = reliantv1.EntityType_ENTITY_TYPE_SYSTEM
)

// UserUpdate represents an update in the user_updates table
// This is for workspace-level updates pushed via the global WebSocket
type UserUpdate struct {
	ID             string               `json:"id"`
	UserID         string               `json:"user_id"`
	SequenceNumber int64                `json:"sequence_number"`
	ProjectID      *string              `json:"project_id,omitempty"`
	WorktreeID     *string              `json:"worktree_id,omitempty"`
	ChatID         *string              `json:"chat_id,omitempty"`
	UpdateType     UserUpdateType       `json:"update_type"`
	EntityType     UserUpdateEntityType `json:"entity_type"`
	EntityID       string               `json:"entity_id"`
	Data           json.RawMessage      `json:"data"`
	CreatedAt      time.Time            `json:"created_at"`
}

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

// BackgroundProcessSourceType identifies how the process was started
type BackgroundProcessSourceType string

const (
	BgProcessSourceLLM            BackgroundProcessSourceType = "llm"
	BgProcessSourcePackageCommand BackgroundProcessSourceType = "package_command"
	BgProcessSourceManual         BackgroundProcessSourceType = "manual"
)

// BackgroundProcessOutputLine represents a single line of process output in the DB.
type BackgroundProcessOutputLine struct {
	ID        int64     `json:"id"`
	ProcessID string    `json:"process_id"`
	Seq       int64     `json:"seq"`
	Stream    string    `json:"stream"` // "stdout" or "stderr"
	Line      string    `json:"line"`
	CreatedAt time.Time `json:"created_at"`
}

// BackgroundProcess represents a persisted background process record
type BackgroundProcess struct {
	ID          string                      `json:"id"`
	PID         *int                        `json:"pid,omitempty"`
	Command     string                      `json:"command"`
	WorkingDir  string                      `json:"working_dir"`
	WorktreeID  *string                     `json:"worktree_id,omitempty"`
	ProjectID   *string                     `json:"project_id,omitempty"`
	ChatID      *string                     `json:"chat_id,omitempty"`
	UserID      string                      `json:"user_id"`
	Status      BackgroundProcessStatus     `json:"status"`
	ExitCode    *int                        `json:"exit_code,omitempty"`
	StartedAt   time.Time                   `json:"started_at"`
	EndedAt     *time.Time                  `json:"ended_at,omitempty"`
	Signature   *string                     `json:"signature,omitempty"`
	SourceType  BackgroundProcessSourceType `json:"source_type"`
	PackageType *string                     `json:"package_type,omitempty"`
	CommandName *string                     `json:"command_name,omitempty"`
	CreatedAt   time.Time                   `json:"created_at"`
	UpdatedAt   time.Time                   `json:"updated_at"`
}

// WorkflowStatus represents the status of a workflow execution.
type WorkflowStatus = core.WorkflowStatus

const (
	WorkflowStatusPending   WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_PENDING
	WorkflowStatusRunning   WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_RUNNING
	WorkflowStatusCompleted WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_COMPLETED
	WorkflowStatusFailed    WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_FAILED
	WorkflowStatusCancelled WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_CANCELLED
	WorkflowStatusPaused    WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_PAUSED
	WorkflowStatusExpired   WorkflowStatus = reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_EXPIRED
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

// Thread lifecycle statuses, mirroring CHAT_WORKFLOW_STATUS.
const (
	ThreadStatusRunning   = core.ThreadStatusRunning
	ThreadStatusCompleted = core.ThreadStatusCompleted
	ThreadStatusFailed    = core.ThreadStatusFailed
	ThreadStatusCancelled = core.ThreadStatusCancelled
	ThreadStatusExpired   = core.ThreadStatusExpired
)

// ContextWindow is an alias to the shared core context-window model.
type ContextWindow = core.ContextWindow

// WorkflowDraft is an alias to the shared core workflow draft model.
type WorkflowDraft = core.WorkflowDraft

// WorkflowScenario is an alias to the shared core workflow scenario model.
type WorkflowScenario = core.WorkflowScenario

// StepExecution is an alias to the shared core step-execution model.
type StepExecution = core.StepExecution

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
// Question Types
// ============================================================================

// QuestionStatus represents the status of a question (stored as INTEGER in DB)
const (
	QuestionStatusPending  = 1 // QUESTION_STATUS_PENDING
	QuestionStatusResolved = 2 // QUESTION_STATUS_RESOLVED
)

// Question represents a pending question for user interaction
type Question struct {
	ID                 string
	ChatID             string
	WorkflowID         string
	TemporalWorkflowID string
	ThreadID           string
	StepID             string
	LoopNodeID         *string
	LoopIteration      *int
	Status             int
	Metadata           *string
	ResponseData       *string
	CreatedAt          time.Time
	ResolvedAt         *time.Time
	ToolCallID         *string
}
