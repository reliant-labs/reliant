// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"time"

	core "github.com/reliant-labs/reliant/internal/db/core"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// Repository defines the interface for all database operations
type Repository interface {
	// Chats
	CreateChat(ctx context.Context, chat *Chat) error
	GetChat(ctx context.Context, id string) (*Chat, error)
	GetChatWithUserCheck(ctx context.Context, id string, userID string) (*Chat, error)
	ListChats(ctx context.Context, filters ChatFilters) ([]*Chat, error)
	SearchChats(ctx context.Context, filters ChatSearchFilters) ([]*Chat, error)
	UpdateChat(ctx context.Context, chat *Chat) error
	DeleteChat(ctx context.Context, id string) error
	ListArchivedChats(ctx context.Context, userID string) ([]*ArchivedChatInfo, error)

	// Messages
	// WARNING: Prefer SaveMessageToThread for most use cases. CreateMessage requires manual
	// Ordinal and ContextSequence which is error-prone. Only use for workflow activities.
	CreateMessage(ctx context.Context, msg *Message) error
	CreateMessageIfNotExists(ctx context.Context, msg *Message) error // INSERT OR IGNORE - same warning
	GetMessage(ctx context.Context, id string) (*Message, error)
	GetMessageByActivityID(ctx context.Context, chatID, activityID string) (*Message, error)
	GetMessageByWorkflowAndActivityID(ctx context.Context, chatID, workflowID, activityID string) (*Message, error)
	GetLatestMessageInThread(ctx context.Context, threadID string) (*Message, error)
	GetMaxContextSequenceInThread(ctx context.Context, threadID string) (int, error)
	GetLatestMessageWithTokensInThread(ctx context.Context, threadID string, contextSequence int) (*Message, error)
	CountMessagesInThread(ctx context.Context, threadID string) (int, error)
	// GetEffectiveMessageCount returns message count including inherited messages from parent branches.
	GetEffectiveMessageCount(ctx context.Context, chatID, threadID string) (int, error)
	GetNextOrdinal(ctx context.Context, threadID string) (int64, error)
	ListMessages(ctx context.Context, chatID string, opts MessageListOptions) ([]*Message, error)
	// GetMessagesByContextWindow loads messages from a specific context window.
	// Used by the threads package for fork chain resolution.
	GetMessagesByContextWindow(ctx context.Context, contextWindowID string, maxOrdinal *int64) ([]*Message, error)
	UpdateMessage(ctx context.Context, msg *Message) error
	UpdateMessageFields(ctx context.Context, messageID string, updates map[string]interface{}) error
	MarkMessageBlocksComplete(ctx context.Context, messageID string) error
	DeleteMessage(ctx context.Context, messageID string) error

	// SaveMessageToThread atomically creates a message with text and optional image content blocks.
	// This is the PREFERRED method for saving messages outside of workflow activities.
	// It automatically handles:
	//   - Getting the next ordinal for the thread
	//   - Determining the correct context_sequence (including fork inheritance)
	//   - Creating content blocks atomically
	//   - Creating chat_update for frontend streaming
	//
	// Use this instead of CreateMessage directly to avoid context_sequence bugs.
	// attachmentIDs are optional - if provided, image blocks are created for each attachment.
	// displayStyle is optional - if provided, sets the UI display style (info/warning/success/hidden).
	SaveMessageToThread(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32) (*Message, error)

	// Content Blocks
	CreateContentBlock(ctx context.Context, block *MessageContentBlock) error
	CreateContentBlockIfNotExists(ctx context.Context, block *MessageContentBlock) error // INSERT OR IGNORE variant
	GetContentBlock(ctx context.Context, id string) (*MessageContentBlock, error)
	GetContentBlockByToolCallID(ctx context.Context, toolCallID string) (*MessageContentBlock, error)
	ListContentBlocks(ctx context.Context, messageID string) ([]*MessageContentBlock, error)
	ListContentBlocksForMessages(ctx context.Context, messageIDs []string) ([]*MessageContentBlock, error)
	UpdateContentBlock(ctx context.Context, block *MessageContentBlock) error
	AppendToContentBlock(ctx context.Context, blockID string, delta string) error
	AppendContentBlockDelta(ctx context.Context, chatID string, blockID string, delta string) error
	AppendToolInputDelta(ctx context.Context, chatID string, blockID string, delta string) error

	// Projects
	CreateProject(ctx context.Context, project *Project) error
	GetProject(ctx context.Context, id string) (*Project, error)
	GetProjectByPath(ctx context.Context, path string) (*Project, error)
	GetProjectByPathAndUser(ctx context.Context, path, userID string) (*Project, error)
	GetProjectByRemoteURLAndUser(ctx context.Context, remoteURL, userID string) (*Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*Project, error)
	ListProjects(ctx context.Context, filters ProjectFilters) ([]*Project, error)
	UpdateProject(ctx context.Context, project *Project, userID string) error
	TouchProject(ctx context.Context, id string, userID string) error
	DeleteProject(ctx context.Context, id string, userID string) error
	GetProjectConfigRecord(ctx context.Context, projectID string) (*ProjectConfigRecord, error)

	// Project ↔ Daemon installations (which daemons have a clone of a project).
	UpsertProjectDaemon(ctx context.Context, projectID, daemonID, path string, defaultBranch *string) error
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error)
	ListProjectDaemonsForDaemon(ctx context.Context, daemonID string) ([]*core.ProjectDaemon, error)
	DeleteProjectDaemon(ctx context.Context, projectID, daemonID string) error

	// Worktrees
	CreateWorktree(ctx context.Context, worktree *Worktree) error
	GetWorktree(ctx context.Context, id string) (*Worktree, error)
	GetWorktreeByPath(ctx context.Context, path string) (*Worktree, error)
	ListWorktrees(ctx context.Context, filters WorktreeFilters) ([]*Worktree, error)
	UpdateWorktree(ctx context.Context, worktree *Worktree) error
	UpdateWorktreeCleanupMetadata(ctx context.Context, id string, metadata *CleanupMetadata) error
	DeleteWorktree(ctx context.Context, id string) error
	ArchiveWorktree(ctx context.Context, id string) error
	UnarchiveWorktree(ctx context.Context, id string) error

	// Repos (nested git repositories within a project)
	CreateRepo(ctx context.Context, repo *core.Repo) error
	GetRepo(ctx context.Context, id string) (*core.Repo, error)
	GetRepoByProjectAndPath(ctx context.Context, projectID, relativePath string) (*core.Repo, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	UpdateRepo(ctx context.Context, repo *core.Repo) error
	DeleteRepo(ctx context.Context, id string) error

	// Plans
	CreatePlan(ctx context.Context, plan *Plan) error
	GetPlan(ctx context.Context, id string) (*Plan, error)
	GetPlanByThreadID(ctx context.Context, threadID string) (*Plan, error)
	ListPlansByThread(ctx context.Context, threadID string) ([]*Plan, error)
	ListPlansByChatID(ctx context.Context, chatID string) ([]*Plan, error)
	ListPlansByProject(ctx context.Context, projectID string) ([]*Plan, error)
	UpdatePlan(ctx context.Context, plan *Plan) error
	UpdatePlanStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error
	DeletePlan(ctx context.Context, id string) error

	// Tasks
	CreateTask(ctx context.Context, task *Task) error
	GetTask(ctx context.Context, id string) (*Task, error)
	GetTaskByPosition(ctx context.Context, planID string, position int) (*Task, error)
	ListTasksByPlan(ctx context.Context, planID string) ([]*Task, error)
	ListTasksByParent(ctx context.Context, parentID string) ([]*Task, error)
	ListRootTasksByPlan(ctx context.Context, planID string) ([]*Task, error)
	GetTaskStatsByPlan(ctx context.Context, planID string) (*TaskStats, error)
	UpdateTask(ctx context.Context, task *Task) error
	UpdateTaskStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error
	DeleteTask(ctx context.Context, id string) error

	// Task Dependencies
	CreateTaskDependency(ctx context.Context, dep *TaskDependency) error
	GetTaskDependency(ctx context.Context, id string) (*TaskDependency, error)
	ListTaskDependenciesByTask(ctx context.Context, taskID string) ([]*TaskDependency, error)
	ListBlockersForTask(ctx context.Context, taskID string) ([]*TaskDependency, error)
	ListDependenciesByPlan(ctx context.Context, planID string) ([]*TaskDependency, error)
	DeleteTaskDependency(ctx context.Context, id string) error
	DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error

	// Settings
	CreateSetting(ctx context.Context, setting *Setting) error
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*Setting, error)
	ListSettings(ctx context.Context, userID string, projectID *string) ([]*Setting, error)
	ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*Setting, error)
	UpdateSetting(ctx context.Context, setting *Setting) error
	DeleteSetting(ctx context.Context, id string) error

	// Settings Helpers
	GetStringOrDefault(ctx context.Context, userID string, projectID *string, key, defaultVal string) (string, error)
	GetBoolOrDefault(ctx context.Context, userID string, projectID *string, key string, defaultVal bool) (bool, error)
	GetIntOrDefault(ctx context.Context, userID string, projectID *string, key string, defaultVal int) (int, error)

	// Visibility (for controlling visibility of workflows/presets in pickers)
	// User-specific overrides take precedence over system defaults
	GetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) (*bool, error)
	ListVisibilityOverrides(ctx context.Context, userID string, itemType int32) (map[string]bool, error)
	SetVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string, isVisible bool) error
	DeleteVisibilityOverride(ctx context.Context, userID string, itemType int32, slug string) error
	// System defaults (seeded by migrations)
	GetItemDefault(ctx context.Context, itemType int32, slug string) (*ItemDefault, error)
	ListHiddenItemDefaults(ctx context.Context, itemType int32) ([]string, error)
	// Convenience method: combines defaults + user overrides
	IsItemVisible(ctx context.Context, userID string, itemType int32, slug string) (bool, error)
	// Default preset assignments for workflows (seeded by migrations)
	GetDefaultPresetAssignments(ctx context.Context, workflowName string) (map[string]string, error)
	GetFloatOrDefault(ctx context.Context, userID string, projectID *string, key string, defaultVal float64) (float64, error)
	SetString(ctx context.Context, userID string, projectID *string, key, value string) error
	SetBool(ctx context.Context, userID string, projectID *string, key string, value bool) error
	SetInt(ctx context.Context, userID string, projectID *string, key string, value int) error
	SetFloat(ctx context.Context, userID string, projectID *string, key string, value float64) error
	GetProviderAPIKey(ctx context.Context, userID string, provider string) (string, error)
	SetProviderAPIKey(ctx context.Context, userID string, provider, apiKey string) error
	DeleteProviderAPIKey(ctx context.Context, userID string, provider string) error
	GetProviderAPIKeys(ctx context.Context, userID string) (map[string]string, error)

	GetCodexAuthTokens(ctx context.Context, userID string) (*core.CodexAuthTokens, error)
	SetCodexAuthTokens(ctx context.Context, userID string, tokens core.CodexAuthTokens) error
	DeleteCodexAuthTokens(ctx context.Context, userID string) error

	GetClaudeAuthTokens(ctx context.Context, userID string) (*core.ClaudeAuthTokens, error)
	SetClaudeAuthTokens(ctx context.Context, userID string, tokens core.ClaudeAuthTokens) error
	DeleteClaudeAuthTokens(ctx context.Context, userID string) error

	// Privacy Settings
	GetAnalyticsEnabled(ctx context.Context, userID string) (bool, error)
	SetAnalyticsEnabled(ctx context.Context, userID string, enabled bool) error
	GetCrashReportingEnabled(ctx context.Context, userID string) (bool, error)
	SetCrashReportingEnabled(ctx context.Context, userID string, enabled bool) error
	GetPrivacySettings(ctx context.Context, userID string) (analyticsEnabled, crashReportingEnabled bool, err error)
	SetPrivacySettings(ctx context.Context, userID string, analyticsEnabled, crashReportingEnabled bool) error

	// Command Favorites (per-project favorited package commands)
	ListCommandFavorites(ctx context.Context, userID, projectID string) ([]string, error)
	AddCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
	RemoveCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error

	// Approvals (consolidated tool and workflow approvals)
	CreateApproval(ctx context.Context, approval *Approval) error
	GetApproval(ctx context.Context, id string) (*Approval, error)
	GetApprovalByEntityID(ctx context.Context, entityID string) (*Approval, error)
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*Approval, error)
	ListApprovalsByChat(ctx context.Context, chatID string) ([]*Approval, error)
	UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string) error

	// Daemon Registry + Config Snapshots
	UpsertDaemon(ctx context.Context, daemon *Daemon) error
	GetDaemon(ctx context.Context, id string) (*Daemon, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*Daemon, error)
	UpsertDaemonAttachment(ctx context.Context, att *DaemonAttachment) error
	TouchDaemonAttachmentIfNewer(ctx context.Context, daemonID string, activityAt time.Time) error
	DeleteDaemonAttachment(ctx context.Context, daemonID string) error
	IsDaemonAttached(ctx context.Context, userID string, staleThreshold time.Duration) (bool, error)
	ListAttachedDaemonIDsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]string, error)
	ListOutboundAttachments(ctx context.Context) ([]*DaemonAttachment, error)
	UpsertProjectConfigRecord(ctx context.Context, record *ProjectConfigRecord) error

	// Daemon PATs (Personal Access Tokens)
	CreateDaemonPAT(ctx context.Context, pat *DaemonPAT) error
	GetDaemonPATByTokenHash(ctx context.Context, tokenHash string) (*DaemonPAT, error)
	ListDaemonPATsByUserID(ctx context.Context, userID string) ([]*DaemonPAT, error)
	RevokeDaemonPAT(ctx context.Context, id string) error
	RevokeDaemonPATsByUserID(ctx context.Context, userID string, ephemeralOnly bool) error
	// RevokeDaemonPATsByDaemonID marks every live (not-yet-revoked) PAT bound to
	// daemonID as revoked. Used by the managed-daemon lifecycle to invalidate a
	// pod's credentials when it is torn down or re-provisioned. Returns the number
	// of rows transitioned to revoked.
	RevokeDaemonPATsByDaemonID(ctx context.Context, daemonID string) (int, error)
	UpdateDaemonPATLastUsed(ctx context.Context, id string) error

	// Sequence-based Synchronization for Polling (per-chat)
	GetLatestUpdateSequence(ctx context.Context, chatID string) (int64, error)
	GetUpdatesSince(ctx context.Context, chatID string, sinceSeq int64, limit int) ([]ChatUpdate, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	GetNextSequenceNumber(ctx context.Context, chatID string) (int64, error)

	// Question methods
	CreateQuestion(ctx context.Context, question *Question) error
	GetQuestionByID(ctx context.Context, id string) (*Question, error)
	GetPendingQuestionByChatID(ctx context.Context, chatID string) (*Question, error)
	GetQuestionsByWorkflowStepIteration(ctx context.Context, workflowID, stepID string, iteration int) ([]*Question, error)
	ResolveQuestion(ctx context.Context, id string, responseData *string) error

	// Typed Chat Update Emitters (type-safe alternatives to CreateChatUpdate)
	EmitQuestionUpdate(ctx context.Context, chatID string, update QuestionUpdate) error
	EmitToolCallUpdate(ctx context.Context, chatID string, update ToolCallUpdate) error
	EmitToolCallCancelledUpdate(ctx context.Context, chatID string, update ToolCallUpdate) error
	EmitToolCallBackgroundedUpdate(ctx context.Context, chatID string, update ToolCallUpdate) error
	// Refetch Signals (tell frontend to re-fetch specific data)
	EmitUserRefetch(ctx context.Context, userID string, refetchType RefetchType, opts RefetchOpts) error
	EmitChatRefetch(ctx context.Context, chatID string, refetchType RefetchType) error

	// User Updates (global WebSocket for workspace-level updates)
	GetLatestUserUpdateSequence(ctx context.Context, userID string) (int64, error)
	GetUserUpdatesSince(ctx context.Context, userID string, sinceSeq int64, limit int) ([]UserUpdate, error)
	CreateUserUpdate(ctx context.Context, update *UserUpdate) error
	// Chat State Management
	UpdateChatState(ctx context.Context, chatID string, state ChatState, reason string) error
	UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error
	UpdateChatActiveDaemon(ctx context.Context, chatID string, daemonID *string) error

	// Background Processes
	CreateBackgroundProcess(ctx context.Context, process *BackgroundProcess) error
	GetBackgroundProcess(ctx context.Context, id string) (*BackgroundProcess, error)
	ListBackgroundProcesses(ctx context.Context, filters BackgroundProcessFilters) ([]*BackgroundProcess, error)
	UpdateBackgroundProcessStatus(ctx context.Context, id string, status BackgroundProcessStatus, exitCode *int, endedAt *time.Time) error
	UpdateBackgroundProcessPID(ctx context.Context, id string, pid int, signature string) error
	GetRunningBackgroundProcesses(ctx context.Context) ([]*BackgroundProcess, error)
	MarkStaleProcesses(ctx context.Context, processIDs []string) error
	CleanupOldBackgroundProcesses(ctx context.Context, olderThan time.Time) (int64, error)

	// Background process output
	CreateBackgroundProcessOutputBatch(ctx context.Context, lines []BackgroundProcessOutputLine) error
	GetBackgroundProcessOutput(ctx context.Context, processID string, afterSeq int64, limit int) ([]BackgroundProcessOutputLine, error)

	// Recovery Helpers
	GetToolResultBlock(ctx context.Context, toolCallID string) (*MessageContentBlock, error)

	// Attachments
	CreateAttachment(ctx context.Context, attachment *Attachment) error
	GetAttachment(ctx context.Context, id string) (*Attachment, error)
	GetAttachmentsByIDs(ctx context.Context, ids []string) ([]*Attachment, error)
	DeleteAttachment(ctx context.Context, id string) error

	// Context Usage (for compaction indicator)
	GetContextUsage(ctx context.Context, chatID, thread string) (*ContextUsage, error)

	// GetThreadTokenCount returns the cumulative token count for a thread.
	// This handles fork inheritance automatically by walking up the fork chain.
	//
	// Parameters:
	// - thread: the thread ID
	// - maxOrdinal: optional - if set, returns tokens at that ordinal (for fork points)
	//               if nil, returns current tokens (most recent message with data)
	//
	// Returns 0 if no token data exists (caller should estimate if needed).
	GetThreadTokenCount(ctx context.Context, thread string, maxOrdinal *int64) (int64, error)

	// Workflows (parent-child hierarchy tracking)
	CreateWorkflow(ctx context.Context, workflow *Workflow) error
	GetWorkflow(ctx context.Context, id string) (*Workflow, error)
	GetWorkflowByThread(ctx context.Context, chatID, thread string) (*Workflow, error)
	ListWorkflowsByChat(ctx context.Context, chatID string) ([]*Workflow, error)
	ListChildWorkflows(ctx context.Context, parentID string) ([]*Workflow, error)
	ListRootWorkflows(ctx context.Context, chatID string) ([]*Workflow, error)
	GetRootWorkflowStatusForChats(ctx context.Context, chatIDs []string) (map[string]WorkflowStatus, error) // Returns map of chatID -> root workflow status
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus WorkflowStatus) (bool, error)
	UpdateWorkflowStatus(ctx context.Context, id string, status WorkflowStatus) error
	EnsureWorkflowRunning(ctx context.Context, workflowID, chatID string)         // Idempotent: no-op if already running
	UpdateWorkflowName(ctx context.Context, id string, workflowName string) error // Only allowed when status is 'pending'
	CompleteChildWorkflows(ctx context.Context, parentWorkflowID string) error    // Cascade completion to all child workflows
	PauseRunningWorkflowsByChat(ctx context.Context, chatID string) error         // Pause all running workflows for a chat
	ResumeWorkflowsByChat(ctx context.Context, chatID string) error               // Resume all paused workflows for a chat
	DeleteWorkflow(ctx context.Context, id string) error
	DeleteWorkflowsByChat(ctx context.Context, chatID string) error
	// Startup recovery queries
	ListWorkflowsByStatus(ctx context.Context, status WorkflowStatus) ([]*Workflow, error)
	ListRootWorkflowsByStatus(ctx context.Context, status WorkflowStatus) ([]*Workflow, error)
	// Threads - First-class entity for thread hierarchy and fork relationships
	// Thread type is derived: NULL parent = root, parent in same conversation = sub_agent,
	// parent in different conversation = branch
	CreateThread(ctx context.Context, thread *Thread) (*Thread, error)
	GetThread(ctx context.Context, id string) (*Thread, error)
	GetThreadByWorkflow(ctx context.Context, workflowID string) (*Thread, error)
	GetRootThread(ctx context.Context, conversationID string) (*Thread, error)
	GetThreadWithParent(ctx context.Context, id string) (*Thread, *string, error) // Returns thread and parent's conversation_id
	ListThreadsByConversation(ctx context.Context, conversationID string) ([]*Thread, error)
	ListChildThreads(ctx context.Context, parentThreadID string) ([]*Thread, error)
	UpdateThreadWorkflow(ctx context.Context, threadID, workflowID string) (*Thread, error)
	UpdateThreadForkPoint(ctx context.Context, threadID string, forkAtOrdinal *int64, forkAtContextWindowID *string) (*Thread, error)
	DeleteThread(ctx context.Context, id string) error
	DeleteThreadsByConversation(ctx context.Context, conversationID string) error
	CountThreadsInConversation(ctx context.Context, conversationID string) (int64, error)

	// Context Windows - Atomic unit for what gets sent to the LLM
	// Each thread has one or more context windows (sequence increments on compaction)
	CreateContextWindow(ctx context.Context, cw *ContextWindow) (*ContextWindow, error)
	GetContextWindow(ctx context.Context, id string) (*ContextWindow, error)
	GetLatestContextWindow(ctx context.Context, threadID string) (*ContextWindow, error)
	GetContextWindowBySequence(ctx context.Context, threadID string, sequence int) (*ContextWindow, error)
	GetContextWindowWithThread(ctx context.Context, id string) (*ContextWindow, string, *string, *int64, error) // Returns cw, conversationID, parentThreadID, forkAtOrdinal
	ListContextWindowsByThread(ctx context.Context, threadID string) ([]*ContextWindow, error)
	GetMaxSequenceForThread(ctx context.Context, threadID string) (int, error)
	SetCompactionSummaryMessage(ctx context.Context, cwID, messageID string) (*ContextWindow, error)
	DeleteContextWindow(ctx context.Context, id string) error
	DeleteContextWindowsByThread(ctx context.Context, threadID string) error

	// Workflow Drafts - User-owned workflows (available across all projects)
	// Project-specific workflows come from .reliant/workflows/*.yaml files (read-only)
	// A workflow is "usable" when is_valid=true AND is_hidden=false.
	CreateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error
	UpsertWorkflowDraft(ctx context.Context, draft *WorkflowDraft) (*WorkflowDraft, error)
	GetWorkflowDraft(ctx context.Context, id string) (*WorkflowDraft, error)
	GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error)
	GetWorkflowDraftByName(ctx context.Context, userID, name string) (*WorkflowDraft, error)
	GetWorkflowDraftByChatID(ctx context.Context, chatID string) (*WorkflowDraft, error)
	GetWorkflowDraftBySourcePath(ctx context.Context, userID, sourcePath string) (*WorkflowDraft, error)
	GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*WorkflowDraft, error)
	ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*WorkflowDraft, error)
	ListUsableWorkflowsByUser(ctx context.Context, userID string) ([]*WorkflowDraft, error)
	UpdateWorkflowDraft(ctx context.Context, draft *WorkflowDraft) error
	UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, isValid bool, validationErrors *string) error
	SetWorkflowDraftHidden(ctx context.Context, id string, isHidden bool) (*WorkflowDraft, error)
	DeleteWorkflowDraft(ctx context.Context, id string) error
	DeleteWorkflowDraftBySlug(ctx context.Context, userID, slug string) error
	WorkflowSlugExists(ctx context.Context, userID, slug string) (bool, error)
	CountWorkflowDraftsByUser(ctx context.Context, userID string) (int64, error)
	AssociateChatWithDraft(ctx context.Context, draftID string, chatID string) (*WorkflowDraft, error)
	UpdateWorkflowForkedFrom(ctx context.Context, draftID string, forkedFrom string) (*WorkflowDraft, error)

	// Workflow Scenarios - Test scenarios for workflows
	CreateWorkflowScenario(ctx context.Context, scenario *WorkflowScenario) error
	GetWorkflowScenario(ctx context.Context, id string) (*WorkflowScenario, error)
	GetWorkflowScenarioByName(ctx context.Context, workflowDraftID string, name string) (*WorkflowScenario, error)
	ListWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) ([]*WorkflowScenario, error)
	ListWorkflowScenariosByUser(ctx context.Context, userID string) ([]*WorkflowScenario, error)
	UpdateWorkflowScenario(ctx context.Context, scenario *WorkflowScenario) error
	UpdateWorkflowScenarioResult(ctx context.Context, id string, status string, result string) error
	DeleteWorkflowScenario(ctx context.Context, id string) error
	DeleteWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) error

	// Presets - User-created presets (available globally or per-project)
	CreatePreset(ctx context.Context, preset *Preset) (*Preset, error)
	UpsertPreset(ctx context.Context, preset *Preset) (*Preset, error)
	GetPreset(ctx context.Context, id string) (*Preset, error)
	GetPresetBySlug(ctx context.Context, userID, slug string) (*Preset, error)
	GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*Preset, error)
	ListUserPresets(ctx context.Context, userID string) ([]*Preset, error)
	ListUserPresetsGlobal(ctx context.Context, userID string) ([]*Preset, error)
	ListUserPresetsByProject(ctx context.Context, userID, projectID string) ([]*Preset, error)
	ListPresetsByTag(ctx context.Context, userID, tag, projectID string) ([]*Preset, error)
	UpdatePreset(ctx context.Context, preset *Preset) (*Preset, error)
	DeletePreset(ctx context.Context, id string) error
	DeletePresetBySlug(ctx context.Context, userID, slug string) error
	DeletePresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) error

	// Step Executions (for CEL history queries)
	CreateStepExecution(ctx context.Context, exec *StepExecution) error
	GetStepExecution(ctx context.Context, id string) (*StepExecution, error)
	GetStepExecutionsByWorkflow(ctx context.Context, workflowID string) ([]*StepExecution, error)
	GetStepExecutionsByStep(ctx context.Context, workflowID, stepID string) ([]*StepExecution, error)
	DeleteStepExecutionsByWorkflow(ctx context.Context, workflowID string) error

	// Node Execution Events (for real-time UI streaming)
	EmitNodeExecutionEvent(ctx context.Context, eventType string, state *NodeExecutionState) error

	// Transactions
	RunTx(ctx context.Context, f func(ctx context.Context) error) error

	// Lifecycle
	Close() error

	// Health
	Ping(ctx context.Context) error
}

// ContextUsage contains context usage info for the compaction indicator
type ContextUsage struct {
	ThreadTokenCount    int64 `json:"thread_token_count"`   // Sum of output_tokens in current context
	CompactionThreshold int64 `json:"compaction_threshold"` // Threshold at which compaction triggers
	CurrentContextSeq   int64 `json:"current_context_seq"`  // Current context_sequence
}

// ChatFilters contains options for filtering chats.
type ChatFilters = core.ChatFilters

// ChatSearchFilters contains options for searching chats.
type ChatSearchFilters = core.ChatSearchFilters

// MessageListOptions contains options for filtering messages.
type MessageListOptions = core.MessageListOptions

// ProjectFilters contains options for filtering projects.
type ProjectFilters = core.ProjectFilters

// WorktreeFilters contains options for filtering worktrees.
type WorktreeFilters = core.WorktreeFilters

// BackgroundProcessFilters contains options for filtering background processes
type BackgroundProcessFilters struct {
	UserID     string
	WorktreeID *string
	ProjectID  *string
	ChatID     *string
	Status     *BackgroundProcessStatus
	SourceType *BackgroundProcessSourceType
	Limit      int
	Offset     int
}

// Attachment represents a file attachment.
// Kept as a facade alias for external API compatibility.
type Attachment = core.Attachment
