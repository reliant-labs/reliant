// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what each
// activity, and each helper several activities share, needs from the
// database; mock_gen.go mocks them. *db.Repo satisfies every one, and
// activities.NewActivities hands it to each constructor.
//
// A role lists the methods its code calls directly, then embeds the role of
// every callee it hands the store to (threads.NewService, messageconv,
// runevents, db.UpsertToolCallStatus, a helper below, ...), so a callee that
// needs more points the compiler here.
package handlers

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	core "github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolbindings"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
	"github.com/reliant-labs/reliant/internal/workflow/messageconv"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
)

// ============================================================================
// Shared helper roles
// ============================================================================

// RunOwnerReader is what resolveRunOwner reads: the parent run, then the chat.
type RunOwnerReader interface {
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// WorktreeOwnerReader is what worktreeOwnerOf reads.
type WorktreeOwnerReader interface {
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
}

// ThreadWorktreeReader is what effectiveWorktreeID reads: the thread's own
// worktree binding.
type ThreadWorktreeReader interface {
	GetThread(ctx context.Context, id string) (*db.Thread, error)
}

// BindingDescriber is what describeBinding reads.
type BindingDescriber interface {
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
}

// RunExecutorContextStore is what resolveRunExecutorContext reads: the chat
// and the project or worktree it runs in.
type RunExecutorContextStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
}

// ToolResultRecoveryStore is what recoverPersistedToolResults reads.
type ToolResultRecoveryStore interface {
	GetToolCallResultForMessage(ctx context.Context, toolCallID string, messageID string) (*db.ToolCallResult, error)
}

// MessageRepairStore is what ConvertAndRepairMessages reads: messageconv's
// conversion, then the persisted tool results the repair recovers.
type MessageRepairStore interface {
	messageconv.Store
	ToolResultRecoveryStore
}

// LLMHistoryStore is what LoadMessagesForLLM reads: the thread's current
// messages (threads.Service) and their conversion.
type LLMHistoryStore interface {
	threads.Repository
	MessageRepairStore
}

// WorkflowErrorWriter is what WriteWorkflowError writes, and all the
// WorkflowError activity needs.
type WorkflowErrorWriter interface {
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
}

// TransitionMessageWriter is what EmitTransitionMessage writes.
type TransitionMessageWriter interface {
	SaveMessageToThread(ctx context.Context, chatID string, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32) (*db.Message, error)
}

// ChatTransitioner is what TransitionChatOnCompletion reads and writes. It
// loads the completed workflow's definition through LoadWorkflowActivity.
type ChatTransitioner interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	UpdateChat(ctx context.Context, chat *db.Chat) error
	LoadWorkflowStore
}

// ============================================================================
// Per-activity stores
// ============================================================================

// ApprovalCreateStore is what ApprovalCreateActivity reads and writes; the
// blocked run event goes through runevents.EmitBlocked.
type ApprovalCreateStore interface {
	GetApprovalByEntityID(ctx context.Context, entityID string) (*db.Approval, error)
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	CreateApproval(ctx context.Context, approval *db.Approval) error
	UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	runevents.Store
}

// ApprovalResolveStore is what ApprovalResolveActivity writes.
type ApprovalResolveStore interface {
	UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string) error
}

// CallLLMStore is what CallLLMActivity reads and writes. It forwards the store
// to the local-model directory, threads.Service, the tool bindings, the mailbox
// drain, history loading and the tool-call status writer.
type CallLLMStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)
	// The spawn tool lists presets from the run's pinned catalog, or its
	// machine's, as LoadPresetParams loads them (projectPresets).
	workflowsource.MachineStore
	workflowsource.PinnedStore
	EmitToolCallUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
	ListQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) ([]*db.AgentMessage, error)
	ListSpawnChildren(ctx context.Context, threadID string) ([]*db.SpawnChild, error)
	ListInheritedSpawnChildren(ctx context.Context, threadID string) ([]*db.InheritedSpawnChild, error)
	RecordProviderBackoff(ctx context.Context, chatID string, threadID string, attempt int, maxAttempts int, statusCode int, reason string, delay time.Duration, now time.Time) error
	ClearProviderBackoff(ctx context.Context, threadID string, now time.Time) error
	// GetDaemon and ListDaemonsByUserID are daemonPlatformReader
	// (resolveShellPlatform).
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	local.Source
	threads.Repository
	toolbindings.SettingsReader
	DrainAgentMessagesStore
	LLMHistoryStore
	db.ToolCallStatusStore
}

// CleanupStore is what CleanupActivity reads and writes when it closes out a
// run: pending approvals, unanswered tool calls and their synthetic results.
type CleanupStore interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error)
	UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string) error
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	ListMessages(ctx context.Context, chatID string, opts db.MessageListOptions) ([]*db.Message, error)
	ListContentBlocks(ctx context.Context, messageID string) ([]*db.MessageContentBlock, error)
	GetToolResultBlock(ctx context.Context, toolCallID string) (*db.MessageContentBlock, error)
	ListToolCallsByIDs(ctx context.Context, toolCallIDs []string) ([]*db.ToolCall, error)
	CreateNumberedMessage(ctx context.Context, msg *db.Message) error
	CreateContentBlock(ctx context.Context, block *db.MessageContentBlock) error
	db.ToolCallStatusStore
}

// CompactStore is what CompactActivity reads and writes; the summary goes
// through threads.Service and the history through LoadMessagesForLLM.
type CompactStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	SetCompactionSummaryMessage(ctx context.Context, cwID string, messageID string) (*db.ContextWindow, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	threads.Repository
	LLMHistoryStore
	// The user's Settings → Model tag preferences, read exactly as CallLLM
	// reads them, so the summary resolves to the model that filled the thread.
	toolbindings.SettingsReader
}

// CreateWorkflowWithThreadStore is what CreateWorkflowWithThreadActivity reads
// beside the threads.Service it is constructed with: the chat and thread, the
// worktree binding it resolves, and the run owner.
type CreateWorkflowWithThreadStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	RunOwnerReader
}

// CreateWorktreeStore is what CreateWorktreeActivity reads and writes; it
// forwards the store to repo.AdoptFromDaemon and workspacecreate.
type CreateWorktreeStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error
	repopkg.AdoptStore
	workspacecreate.Store
	WorktreeOwnerReader
}

// DeleteWorktreeStore is what DeleteWorktreeActivity reads and writes.
type DeleteWorktreeStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	ArchiveWorktree(ctx context.Context, id string) error
}

// DrainAgentMessagesStore is what DrainAgentMessagesActivity reads and writes
// beside the threads.Service it is constructed with.
type DrainAgentMessagesStore interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	ListQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) ([]*db.AgentMessage, error)
	MarkAgentMessagesDelivered(ctx context.Context, ids []string, deliveredAt time.Time, deliveredMessageID string) ([]string, error)
	SetAgentMessagesDeliveredMessageID(ctx context.Context, ids []string, deliveredMessageID string) error
	EmitAgentMessagesDrainedUpdate(ctx context.Context, chatID string, update db.AgentMessagesDrainedUpdate) error
}

// EmitStreamFinalizedStore is what EmitStreamFinalizedActivity writes.
type EmitStreamFinalizedStore interface {
	EmitStreamFinalizedUpdate(ctx context.Context, chatID string, update db.StreamFinalizedUpdate) error
}

// EmitToolCallStatusStore is what EmitToolCallStatusActivity writes.
type EmitToolCallStatusStore interface {
	EmitToolCallUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
	db.ToolCallStatusStore
}

// EnqueueAgentMessageStore is what EnqueueAgentMessageActivity writes.
type EnqueueAgentMessageStore interface {
	EnqueueAgentMessage(ctx context.Context, msg *db.AgentMessage) error
	EnqueueSpawnReport(ctx context.Context, msg *db.AgentMessage) (db.SpawnReportOutcome, error)
}

// ExecuteRunStepStore is what ExecuteRunStepActivity reads and writes.
type ExecuteRunStepStore interface {
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	RunExecutorContextStore
}

// ExecuteToolsStore is what ExecuteToolsActivity (and InvokeToolActivity,
// which wraps it) reads and writes. It forwards the store to the tool
// bindings, the worktree resolution and the tool-call status writer.
type ExecuteToolsStore interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	GetContextUsage(ctx context.Context, chatID string, thread string) (*db.ContextUsage, error)
	GetToolCall(ctx context.Context, id string) (*db.ToolCall, error)
	GetToolCallResult(ctx context.Context, toolCallID string) (*db.ToolCallResult, error)
	UpsertToolCallResult(ctx context.Context, chatID string, result *db.ToolCallResult) error
	SetChatDaemonBlocked(ctx context.Context, chatID string, blocked bool) error
	EmitToolCallUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error
	// MarkQueuedAgentMessagesPreempting is preemptionStore
	// (newBatchPreemption).
	MarkQueuedAgentMessagesPreempting(ctx context.Context, toThreadID string, preemptedAt time.Time) (int64, error)
	toolbindings.SettingsReader
	ThreadWorktreeReader
	db.ToolCallStatusStore
}

// GenerateTitleStore is what GenerateTitleActivity reads and writes.
type GenerateTitleStore interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	UpdateChat(ctx context.Context, chat *db.Chat) error
	CreateUserUpdate(ctx context.Context, update *db.UserUpdate) error
}

// GitPublishStore is what GitPublishActivity reads.
type GitPublishStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	WorktreeOwnerReader
}

// LoadPresetParamsStore is what LoadPresetParamsActivity reads: the run's
// chat, and the presets its machine synced (or the union of every machine's).
type LoadPresetParamsStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)
	workflowsource.MachineStore
	workflowsource.PinnedStore
}

// LoadWorkflowStore is what LoadWorkflowActivity reads: the chat's project or
// worktree, its presets, and the workflow itself through workflowsource, from
// the catalog the run's machine synced (workflowsource.MachineStore).
type LoadWorkflowStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)
	workflowsource.Store
	workflowsource.MachineStore
	workflowsource.PinnedStore
	// PinProjectCatalog stores the catalog a run starts with (db.Repo).
	PinProjectCatalog(ctx context.Context, scope db.CatalogPinScope) (string, error)
}

// MachineResumeWakeStore is what MachineResumeWakeActivity reads and writes.
type MachineResumeWakeStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	// GetWorktree finds the chat's workspace owner (runMachineSelector).
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
	SetChatDaemonBlocked(ctx context.Context, chatID string, blocked bool) error
}

// PreflightDaemonCheckStore is what PreflightDaemonCheckActivity reads and
// writes.
type PreflightDaemonCheckStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	// GetWorktree finds the chat's workspace owner (runMachineSelector).
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
	QueueChatForMachine(ctx context.Context, chatID string, machine *db.QueuedMachine) (bool, error)
	SetChatQueuedForMachine(ctx context.Context, chatID string, queued bool) (bool, error)
	SetChatDaemonBlocked(ctx context.Context, chatID string, blocked bool) error
}

// QuestionCreateStore is what QuestionCreateActivity reads and writes; the
// blocked run event goes through runevents.EmitBlocked, and an async question
// is announced to the owner's inbox instead (GetChat comes with
// runevents.Store).
type QuestionCreateStore interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	GetQuestionsByWorkflowStepIteration(ctx context.Context, workflowID string, stepID string, iteration int) ([]*db.Question, error)
	CreateQuestion(ctx context.Context, question *db.Question) error
	UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error
	EmitQuestionUpdate(ctx context.Context, chatID string, update db.QuestionUpdate) error
	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error
	runevents.Store
}

// QuestionResolveStore is what QuestionResolveActivity reads and writes.
type QuestionResolveStore interface {
	GetQuestionByID(ctx context.Context, id string) (*db.Question, error)
	ResolveQuestion(ctx context.Context, id string, responseData *string) error
	EmitQuestionUpdate(ctx context.Context, chatID string, update db.QuestionUpdate) error
}

// SaveMessageStore is what SaveMessageActivity writes: the message through
// threads.Service and its attachments.
type SaveMessageStore interface {
	CreateAttachment(ctx context.Context, attachment *db.Attachment) error
	threads.Repository
}

// ThreadStatusStore is what ThreadStatusActivity writes.
type ThreadStatusStore interface {
	UpdateThreadStatus(ctx context.Context, threadID string, status db.ThreadStatus, completedAt *time.Time) (*db.Thread, error)
	MarkQueuedAgentMessagesUndeliveredForThread(ctx context.Context, toThreadID string) (int64, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
}

// UsageLimitWaitStore is what UsageLimitWaitActivity writes; its notice goes
// through threads.Service (postNotice).
type UsageLimitWaitStore interface {
	RecordUsageLimitWait(ctx context.Context, chatID string, threadID string, provider string, probing bool, resumeAt time.Time, now time.Time) error
	ClearUsageLimitWait(ctx context.Context, chatID string, threadID string, now time.Time) error
	threads.Repository
}

// ValidateThreadOwnershipStore is what ValidateThreadOwnershipActivity reads.
type ValidateThreadOwnershipStore interface {
	GetThread(ctx context.Context, id string) (*db.Thread, error)
}

// WorkflowCheckpointStore is what WorkflowCheckpointActivity writes.
type WorkflowCheckpointStore interface {
	UpsertWorkflowCheckpoint(ctx context.Context, checkpoint *db.WorkflowCheckpoint) error
}

// WorkflowStatusStore is what WorkflowStatusActivity reads and writes: the run
// and chat status transitions, their cascades, the trigger bookkeeping, and
// the transition to the next workflow. It forwards the store to
// threads.Service, runevents.EmitTerminal and TransitionChatOnCompletion.
type WorkflowStatusStore interface {
	ListRunNodeExecutions(ctx context.Context, workflowID string, limit int) ([]*db.StepExecution, error)
	MarkDeclaredFailureNode(ctx context.Context, workflowID, nodeID string) (bool, error)
	SetLaunchEventRunFailureDetail(ctx context.Context, chatID, detail string) error
	SaveMessageToThreadOnce(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string) (*db.Message, bool, error)
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	CreateWorkflow(ctx context.Context, workflow *db.Workflow) error
	UpdateWorkflowStatus(ctx context.Context, id string, status db.WorkflowStatus) error
	SetWorkflowOutcome(ctx context.Context, id string, outcome string) error
	DeleteWorkflowCheckpoint(ctx context.Context, workflowID string) error
	CascadeTerminalStatusToDescendants(ctx context.Context, parentWorkflowID string, reason db.WorkflowStopReason) error
	CascadeTerminalStatusToThreadSubtree(ctx context.Context, workflowID string, reason db.WorkflowStopReason) error
	PauseRunningWorkflowsByChat(ctx context.Context, chatID string) error
	ResumeWorkflowsByChat(ctx context.Context, chatID string) error
	UpdateThreadWorkflow(ctx context.Context, threadID string, workflowID string) (*db.Thread, error)
	ReviveThread(ctx context.Context, threadID string) (int64, error)
	UpdateChatState(ctx context.Context, chatID string, state db.ChatState, reason string) error
	UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error
	SetChatPausedForMachine(ctx context.Context, chatID string, paused bool) (bool, error)
	SetChatRunID(ctx context.Context, chatID, workflowID, runID string) (bool, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
	FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
	SetLaunchEventRunStatus(ctx context.Context, chatID string, status string) error
	threads.Repository
	runevents.Store
	ChatTransitioner
	TransitionMessageWriter
}
