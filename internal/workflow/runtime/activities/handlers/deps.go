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
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	core "github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolbindings"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
	"github.com/reliant-labs/reliant/internal/workflow/messageconv"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
)

// ============================================================================
// Shared helper roles
// ============================================================================

// WorkflowLoader reads one workflow (run) row by id. store.WorkflowExtStore
// satisfies it.
type WorkflowLoader interface {
	LoadWorkflow(ctx context.Context, id string) (*core.Workflow, error)
}

// WorkspaceRows is the project, worktree, repo and daemon rows the activities
// read by id: where a chat runs, which repos its project holds, and the
// registration of the machine it reaches. Like the reads it replaces it takes
// no owner; each activity reached it from a chat it already holds.
// NewWorkspaceRows builds it over the generated ext stores.
type WorkspaceRows interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
	WorktreeOwnerReader
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

type workspaceRows struct {
	store.ProjectExtStore
	store.WorktreeExtStore
	store.RepoExtStore
	store.DaemonExtStore
}

// NewWorkspaceRows is the WorkspaceRows over the generated ext stores of db.
func NewWorkspaceRows(db orm.Context) WorkspaceRows {
	return workspaceRows{
		ProjectExtStore:  store.NewProjectExtStore(db),
		WorktreeExtStore: store.NewWorktreeExtStore(db),
		RepoExtStore:     store.NewRepoExtStore(db),
		DaemonExtStore:   store.NewDaemonExtStore(db),
	}
}

// WorkflowRuns is what WorkflowStatusActivity reads and ends on a run's rows
// that carry no chat activity: the run itself, its descendants' terminal
// cascade, its position checkpoint and its node attempts. NewWorkflowRuns
// builds it over the generated ext stores.
type WorkflowRuns interface {
	WorkflowLoader
	CascadeTerminalStatusToDescendants(ctx context.Context, parentID string, reason core.WorkflowStopReason) error
	ClearWorkflowCheckpoint(ctx context.Context, workflowID string) error
	ListRunNodeExecutions(ctx context.Context, workflowID string, limit int) ([]*core.StepExecution, error)
}

type workflowRuns struct {
	store.WorkflowExtStore
	store.WorkflowCheckpointExtStore
	store.StepExecutionExtStore
}

// NewWorkflowRuns is the WorkflowRuns over the generated ext stores of db.
func NewWorkflowRuns(db orm.Context) WorkflowRuns {
	return workflowRuns{
		WorkflowExtStore:           store.NewWorkflowExtStore(db),
		WorkflowCheckpointExtStore: store.NewWorkflowCheckpointExtStore(db),
		StepExecutionExtStore:      store.NewStepExecutionExtStore(db),
	}
}

// QuestionsByStep reads the questions one step raised in one loop iteration.
// store.QuestionExtStore satisfies it.
type QuestionsByStep interface {
	ListQuestionsByWorkflowStepIteration(ctx context.Context, workflowID string, stepID string, iteration int) ([]*core.Question, error)
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
	// Installs is each machine's checkout of the project.
	daemonplacement.Installs
}

// ToolResultRecoveryStore is what recoverPersistedToolResults reads: the result
// of the call a given assistant message carries. store.ToolCallResultExtStore
// satisfies it.
type ToolResultRecoveryStore interface {
	FindToolCallResultForMessage(ctx context.Context, toolCallID string, messageID string) (*core.ToolCallResult, error)
}

// MessageRepairStore is what ConvertAndRepairMessages reads: messageconv's
// conversion. The persisted tool results the repair recovers come from a
// ToolResultRecoveryStore beside it.
type MessageRepairStore interface {
	messageconv.Store
}

// LLMHistoryStore is what LoadMessagesForLLM reads: the thread's current
// messages (threads.Service) and their conversion.
type LLMHistoryStore interface {
	threads.Repository
	MessageRepairStore
}

// ToolCalls is the tool-call records the tool-execution path reads and
// writes: a call by id (nil when none), the result recorded for it, and the
// result write. Each takes the transaction the ctx carries. NewToolCalls
// builds it over the generated ext stores.
type ToolCalls interface {
	FindToolCall(ctx context.Context, id string) (*core.ToolCall, error)
	FindToolCallResult(ctx context.Context, toolCallID string) (*core.ToolCallResult, error)
	UpsertToolCallResult(ctx context.Context, chatID string, result *core.ToolCallResult) error
	ToolResultRecoveryStore
}

type toolCalls struct {
	store.ToolCallExtStore
	store.ToolCallResultExtStore
}

// NewToolCalls is the ToolCalls over the generated ext stores of db.
func NewToolCalls(db orm.Context) ToolCalls {
	return toolCalls{
		ToolCallExtStore:       store.NewToolCallExtStore(db),
		ToolCallResultExtStore: store.NewToolCallResultExtStore(db),
	}
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
	UpdateChat(ctx context.Context, chat *db.Chat) error
	LoadWorkflowStore
	// ProjectWorktreeReader is where the completed workflow's definition is
	// found.
	ProjectWorktreeReader
}

// ============================================================================
// Per-activity stores
// ============================================================================

// ApprovalCreateStore is what ApprovalCreateActivity reads and writes; the
// blocked run event goes through runevents.EmitBlocked.
type ApprovalCreateStore interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	CreateApproval(ctx context.Context, approval *db.Approval) error
	UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
}

// ApprovalLookup finds the approval raised for an entity (idempotent raising).
// store.ApprovalExtStore satisfies it.
type ApprovalLookup interface {
	GetApprovalByEntityID(ctx context.Context, entityID string) (*core.Approval, error)
}

// ApprovalCreateDeps is what NewApprovalCreateActivity takes.
type ApprovalCreateDeps struct {
	Repo      ApprovalCreateStore
	Approvals ApprovalLookup
	// RunEvents records the blocked run event (runevents.NewStore).
	RunEvents runevents.Store
}

// ToolCallResults reads the durable result recorded for a tool call.
// store.ToolCallResultExtStore satisfies it.
type ToolCallResults interface {
	FindToolCallResult(ctx context.Context, toolCallID string) (*core.ToolCallResult, error)
}

// TriggerCapacitySlotDeps is what NewTriggerCapacitySlotActivity takes.
type TriggerCapacitySlotDeps struct {
	Slots TriggerCapacitySlotStore
}

// ApprovalResolveStore is what ApprovalResolveActivity writes, and what
// CleanupActivity closes a run's approvals through. approvalclose.Closer
// implements it.
type ApprovalResolveStore interface {
	ResolvePendingApproval(ctx context.Context, id string, status int32, reason, actionTaken string) (bool, error)
}

// CallLLMStore is what CallLLMActivity reads and writes. It forwards the store
// to the local-model directory, threads.Service, the tool bindings, the mailbox
// drain, history loading and the tool-call status writer.
type CallLLMStore interface {
	// AttendanceStore decides who is behind the chat's calls (chatAttendance).
	AttendanceStore
	ResolveThreadAmbientMemoryExclusion(ctx context.Context, chatID, threadID string, requested bool) (bool, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)
	// The spawn tool lists presets from the run's pinned catalog, or its
	// machine's, as LoadPresetParams loads them (projectPresets).
	workflowsource.MachineStore
	workflowsource.PinnedStore
	EmitToolCallUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
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
	// Installs is each machine's checkout of the project (resolveCallCheckout).
	daemonplacement.Installs
}

// ToolResultFinder reads the tool_result block a tool call was answered with.
// store.MessageContentBlockExtStore satisfies it.
type ToolResultFinder interface {
	FindToolBlock(ctx context.Context, toolCallID string, blockType reliantv1.ContentBlockType) (*core.MessageContentBlock, error)
}

// CleanupDeps is what a CleanupActivity is built from.
type CleanupDeps struct {
	// Store is what the activity reads and writes.
	Store CleanupStore
	// ToolBlocks finds the result a tool call already has.
	ToolBlocks ToolResultFinder
	// Approvals closes the run's pending approvals.
	Approvals ApprovalResolveStore
	// Workflows reads a spawn's child run.
	Workflows WorkflowLoader
}

// CleanupStore is what CleanupActivity reads and writes when it closes out a
// run: pending approvals, unanswered tool calls and their synthetic results.
type CleanupStore interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	ListMessages(ctx context.Context, chatID string, opts db.MessageListOptions) ([]*db.Message, error)
	ListContentBlocks(ctx context.Context, messageID string) ([]*db.MessageContentBlock, error)
	ListToolCallsByIDs(ctx context.Context, toolCallIDs []string) ([]*db.ToolCall, error)
	CreateNumberedMessage(ctx context.Context, msg *db.Message) error
	CreateContentBlock(ctx context.Context, block *db.MessageContentBlock) error
	db.ToolCallStatusStore
}

// CompactStore is what CompactActivity reads and writes; the summary goes
// through threads.Service and the history through LoadMessagesForLLM.
type CompactStore interface {
	// AttendanceStore decides who is behind the chat's calls (chatAttendance).
	AttendanceStore
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
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
}

// CreateWorktreeStore is what CreateWorktreeActivity reads and writes; it
// forwards the store to repo.AdoptFromDaemon and workspacecreate.
type CreateWorktreeStore interface {
	repopkg.AdoptStore
	workspacecreate.Store
	// Installs is each machine's checkout of the project: the owner's is
	// the source of a workspace's repos.
	daemonplacement.Installs
}

// DeleteWorktreeStore is what DeleteWorktreeActivity reads and writes.
type DeleteWorktreeStore interface {
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	ArchiveWorktree(ctx context.Context, id string) error
	// Installs is each machine's checkout of the project: the owner's is
	// the source of a workspace's repos.
	daemonplacement.Installs
}

// DrainAgentMessagesStore is what DrainAgentMessagesActivity reads and writes
// beside the threads.Service it is constructed with.
type DrainAgentMessagesStore interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
}

// EmitStreamFinalizedStore is what EmitStreamFinalizedActivity writes.
type EmitStreamFinalizedStore interface {
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
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

// EnqueueAgentMessageDeps is what an EnqueueAgentMessageActivity is built from.
type EnqueueAgentMessageDeps struct {
	// Mailbox queues the message and announces it to the chat's clients.
	// *agentmailbox.Mailbox satisfies it.
	Mailbox EnqueueAgentMessageStore
}

// ExecuteRunStepStore is what ExecuteRunStepActivity reads and writes.
type ExecuteRunStepStore interface {
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	RunExecutorContextStore
}

// WorktreeReviver brings a chat's archived worktree back
// (reviveWorktree, execute_tools_workspace.go). store.WorktreeExtStore
// satisfies it; ReviveArchivedWorktree rechecks eligibility under the row lock.
type WorktreeReviver interface {
	ReviveArchivedWorktree(ctx context.Context, id string) (string, error)
}

// ExecuteToolsStore is what ExecuteToolsActivity (and InvokeToolActivity,
// which wraps it) reads and writes. It forwards the store to the tool
// bindings, the worktree resolution and the tool-call status writer.
type ExecuteToolsStore interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	GetContextUsage(ctx context.Context, chatID string, thread string) (*db.ContextUsage, error)
	SetChatDaemonBlocked(ctx context.Context, chatID string, blocked bool) error
	// CreateAttachment is persistToolMedia's: a tool result's images and PDFs
	// reach the model as attachment rows.
	CreateAttachment(ctx context.Context, attachment *db.Attachment) error
	// GetAttachment is persistToolMedia's too: an attachment id a tool reports
	// in its metadata joins the result only if the chat's user owns it.
	GetAttachment(ctx context.Context, id string) (*db.Attachment, error)
	EmitToolCallUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
	toolbindings.SettingsReader
	ThreadWorktreeReader
	db.ToolCallStatusStore
	// Installs is each machine's checkout of the project (resolveCallCheckout).
	daemonplacement.Installs
	// AttendanceStore decides who is behind a tool call's chat, for the run
	// scope a tool that spends a provider resolves in (runScope).
	AttendanceStore
}

// RunFundingStore is what reading who may pay for a tool call of a chat in
// an org needs (runFunding): the person's per-org setting and the chat's own
// order. NewRunFundingStore builds one.
type RunFundingStore interface {
	GetSettingValue(ctx context.Context, userID, key string) (string, bool)
	// ChatFundingOrder is the order a person chose for the chat; "" when it
	// inherits.
	ChatFundingOrder(ctx context.Context, chatID string) (string, error)
}

// GenerateTitleStore is what GenerateTitleActivity reads and writes.
type GenerateTitleStore interface {
	// AttendanceStore decides who is behind the chat's calls (chatAttendance).
	AttendanceStore
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	UpdateChat(ctx context.Context, chat *db.Chat) error
	CreateUserUpdate(ctx context.Context, update *db.UserUpdate) error
}

// GitPublishStore is what GitPublishActivity reads beside its WorkspaceRows:
// the chat it publishes for.
type GitPublishStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// LoadPresetParamsStore is what LoadPresetParamsActivity reads: the run's
// chat, and the presets its machine synced (or the union of every machine's).
type LoadPresetParamsStore interface {
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)
	workflowsource.MachineStore
	workflowsource.PinnedStore
}

// LoadWorkflowStore is what LoadWorkflowActivity reads: the chat's project or
// worktree, its presets, and the workflow itself through workflowsource, from
// the catalog the run's machine synced (workflowsource.MachineStore).
type LoadWorkflowStore interface {
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)
	workflowsource.Store
	workflowsource.MachineStore
	workflowsource.PinnedStore
	// PinProjectCatalog stores the catalog a run starts with (db.Repo).
	PinProjectCatalog(ctx context.Context, scope db.CatalogPinScope) (string, error)
}

// ProjectWorktreeReader reads where a chat runs: its project, or the worktree
// it runs in (LoadWorkflowActivity, resolveRunExecutorContext). WorkspaceRows
// satisfies it.
type ProjectWorktreeReader interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
}

// MachineResumeWakeStore is what MachineResumeWakeActivity reads and writes.
type MachineResumeWakeStore interface {
	SetChatDaemonBlocked(ctx context.Context, chatID string, blocked bool) error
}

// PreflightDaemonCheckStore is what PreflightDaemonCheckActivity reads and
// writes.
type PreflightDaemonCheckStore interface {
	QueueChatForMachine(ctx context.Context, chatID string, machine *db.QueuedMachine) (bool, error)
	SetChatQueuedForMachine(ctx context.Context, chatID string, queued bool) (bool, error)
	SetChatDaemonBlocked(ctx context.Context, chatID string, blocked bool) error
}

// QuestionCreateStore is what QuestionCreateActivity reads and writes; the
// blocked run event goes through runevents.EmitBlocked, and an async question
// is announced to the owner's inbox instead.
type QuestionCreateStore interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error
}

// QuestionResolveStore is what QuestionResolveActivity reads and writes.
type QuestionResolveStore interface {
	GetQuestionByID(ctx context.Context, id string) (*db.Question, error)
}

// QuestionWriter creates and resolves questions and announces the chat's
// activity. *questions.Service satisfies it.
type QuestionWriter interface {
	CreateQuestion(ctx context.Context, question *db.Question) error
	ResolveQuestion(ctx context.Context, id string, responseData *string) error
}

// SaveMessageStore is what SaveMessageActivity writes: the message through
// threads.Service and its attachments.
type SaveMessageStore interface {
	CreateAttachment(ctx context.Context, attachment *db.Attachment) error
}

// ThreadStatusStore is what ThreadStatusActivity writes.
type ThreadStatusStore interface {
	UpdateThreadStatus(ctx context.Context, threadID string, status db.ThreadStatus, completedAt *time.Time) (*db.Thread, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
}

// UsageLimitWaitStore is what UsageLimitWaitActivity writes; its notice goes
// through threads.Service (postNotice).
type UsageLimitWaitStore interface {
	threads.Repository
}

// ProviderBackoffMarkers writes the markers of a thread parked on a provider:
// the driver's retry ladder and a usage-limit wait. providerbackoff.Service
// satisfies it.
type ProviderBackoffMarkers interface {
	RecordProviderBackoff(ctx context.Context, chatID string, threadID string, attempt int, maxAttempts int, statusCode int, reason string, delay time.Duration, now time.Time) error
	ClearProviderBackoff(ctx context.Context, threadID string, now time.Time) error
	RecordUsageLimitWait(ctx context.Context, chatID string, threadID string, provider string, probing bool, resumeAt time.Time, now time.Time) error
	ClearUsageLimitWait(ctx context.Context, chatID string, threadID string, now time.Time) error
}

// TriggerCapacitySlotStore is what TriggerCapacitySlotActivity writes.
type TriggerCapacitySlotStore interface {
	ParkTriggerCapacity(ctx context.Context, slot store.TriggerCapacitySlot) (bool, error)
	ResumeTriggerCapacity(ctx context.Context, slot store.TriggerCapacitySlot) (bool, bool, error)
}

// ValidateThreadOwnershipStore is what ValidateThreadOwnershipActivity reads.
type ValidateThreadOwnershipStore interface {
	GetThread(ctx context.Context, id string) (*db.Thread, error)
}

// WorkflowCheckpointStore is what WorkflowCheckpointActivity writes.
// store.WorkflowCheckpointExtStore satisfies it.
type WorkflowCheckpointStore interface {
	SaveWorkflowCheckpoint(ctx context.Context, cp *core.WorkflowCheckpoint) error
}

// WorkflowCheckpointDeps is what NewWorkflowCheckpointActivity takes.
type WorkflowCheckpointDeps struct {
	Checkpoints WorkflowCheckpointStore
}

// RunRecords is what WorkflowStatusActivity records about a run: how it ended
// on its row and on the launch event of the chat, the node whose failure the
// run handled, and the run id its chat is executing. The methods are those of
// the generated ext stores (see NewRunRecords); each takes the transaction
// the ctx carries.
type RunRecords interface {
	SetWorkflowOutcome(ctx context.Context, id string, outcome string) error
	SetWorkflowRunOutcome(ctx context.Context, id string, outcome string) error
	SetWorkflowFailureDetail(ctx context.Context, id string, detail string) error
	FirstHandledNodeFailure(ctx context.Context, workflowID string, runID string, since time.Time) (*store.HandledNodeFailure, error)
	MarkDeclaredFailureNode(ctx context.Context, workflowID string, nodeID string) (bool, error)
	SetLaunchEventRunStatus(ctx context.Context, chatID string, status string) error
	SetLaunchEventRunOutcome(ctx context.Context, chatID string, outcome string) error
	SetLaunchEventRunFailureDetail(ctx context.Context, chatID string, detail string) error
	SetChatRunID(ctx context.Context, chatID string, workflowID string, runID string) (bool, error)
}

// AttentionRecorder applies an attention update to its incident in a READ
// COMMITTED transaction (NewAttentionRecorder).
type AttentionRecorder interface {
	ApplyAttentionUpdate(ctx context.Context, update core.AttentionUpdate) (core.AttentionResult, error)
}

// TriggerPauser pauses a trigger with a system reason (core.TriggerPaused*);
// paused is false when it was already paused. store.TriggerWritesExtStore
// satisfies it.
type TriggerPauser interface {
	SystemPauseTrigger(ctx context.Context, id, reason string) (paused bool, err error)
}

// WorkflowStatusDeps is what NewWorkflowStatusActivity takes.
type WorkflowStatusDeps struct {
	Repo WorkflowStatusStore
	// Triggers reads the trigger a run was launched by (NewTriggerReads).
	Triggers StatusTriggers
	// Updates tells the chat's clients to re-read its workflow executions
	// (chatupdates.Service). Nil announces nothing.
	Updates   ChatRefetcher
	Runs      RunRecords
	Workflows WorkflowRuns
	Attention AttentionRecorder
	// RunEvents records the terminal run event (runevents.NewStore).
	RunEvents runevents.Store
	// Pauser pauses a trigger whose unattended run an org policy ended. Nil
	// pauses nothing: the next refused run records the failure only.
	Pauser TriggerPauser
}

type runRecords struct {
	store.WorkflowExtStore
	store.StepExecutionExtStore
	store.TriggerEventExtStore
	store.ChatExtStore
}

// NewRunRecords is the RunRecords over the generated ext stores of db.
func NewRunRecords(db orm.Context) RunRecords {
	return runRecords{
		WorkflowExtStore:      store.NewWorkflowExtStore(db),
		StepExecutionExtStore: store.NewStepExecutionExtStore(db),
		TriggerEventExtStore:  store.NewTriggerEventExtStore(db),
		ChatExtStore:          store.NewChatExtStore(db),
	}
}

// WorkflowStatusStore is what WorkflowStatusActivity reads and writes: the run
// and chat status transitions, their cascades, the trigger bookkeeping, and
// the transition to the next workflow. It forwards the store to
// threads.Service, runevents.EmitTerminal and TransitionChatOnCompletion.
type WorkflowStatusStore interface {
	SaveMessageToThreadOnce(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string) (*db.Message, bool, error)
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	CreateWorkflow(ctx context.Context, workflow *db.Workflow) error
	UpdateWorkflowStatus(ctx context.Context, id string, status db.WorkflowStatus) error
	CascadeTerminalStatusToThreadSubtree(ctx context.Context, workflowID string, reason db.WorkflowStopReason) error
	PauseRunningWorkflowsByChat(ctx context.Context, chatID string) error
	ResumeWorkflowsByChat(ctx context.Context, chatID string) error
	UpdateThreadWorkflow(ctx context.Context, threadID string, workflowID string) (*db.Thread, error)
	ReviveThread(ctx context.Context, threadID string) (int64, error)
	UpdateChatState(ctx context.Context, chatID string, state db.ChatState, reason string) error
	UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error
	SetChatPausedForMachine(ctx context.Context, chatID string, paused bool) (bool, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	threads.Repository
	ChatTransitioner
	TransitionMessageWriter
}

// MessageSaver writes a message through the threads service: the allocation
// of its order, its content blocks and the idempotency checks of a retried
// activity. *threads.Service satisfies it.
type MessageSaver interface {
	SaveMessage(ctx context.Context, opts threads.SaveMessageOpts) (*threads.SaveMessageResult, error)
}

// SaveMessageDeps is what a SaveMessageActivity is built from.
type SaveMessageDeps struct {
	// Store is what the activity reads and writes besides the message.
	Store SaveMessageStore
	// Threads saves the message.
	Threads MessageSaver
}

// CompactDeps is what a CompactActivity is built from.
type CompactDeps struct {
	// Store is what the activity reads and writes.
	Store CompactStore
	// Triggers reads the trigger side of the attendance rule (chatAttendance):
	// NewTriggerReads. Nil decides nothing, so the run is unattended and
	// spends the org's sources only.
	Triggers AttendanceTriggers
	// Threads saves the compaction summary message.
	Threads MessageSaver
	// ToolResults recovers the tool results the history lost.
	ToolResults ToolResultRecoveryStore
	// DriverResolver picks the model driver that writes the summary.
	DriverResolver drivers.DriverResolver
}

// UsageLimitWaitDeps is what a UsageLimitWaitActivity is built from.
type UsageLimitWaitDeps struct {
	// Store records and clears the wait marker.
	Store UsageLimitWaitStore
	// Waits writes and clears the wait marker.
	Waits ProviderBackoffMarkers
	// Threads saves the notice the wait posts to the transcript.
	Threads MessageSaver
}

// MailboxPreempter marks a thread's queued mail as having cut a waiting tool
// call short. store.AgentMessageExtStore satisfies it.
type MailboxPreempter interface {
	MarkQueuedAgentMessagesPreempting(ctx context.Context, toThreadID string, preemptedAt time.Time) (int64, error)
}

// ExecuteToolsDeps is what an ExecuteToolsActivity (and the InvokeTool and
// automation-approval activities that run its path) is built from.
type ExecuteToolsDeps struct {
	// Store is what the activity reads and writes.
	Store ExecuteToolsStore
	// Triggers reads the trigger side of the attendance rule (chatAttendance):
	// NewTriggerReads. Nil decides nothing, so the run is unattended and
	// spends the org's sources only.
	Triggers AttendanceTriggers
	// Rows reads the project, worktree and repo rows a call resolves its
	// workspace from.
	Rows WorkspaceRows
	// ToolCalls reads and writes the tool-call records.
	ToolCalls ToolCalls
	// ToolExecutor runs the tools on the user's machine.
	ToolExecutor toolexec.ToolExecutor
	// Preemption ends a batch's waiting calls when mail arrives for the
	// thread. Nil never preempts.
	Preemption MailboxPreempter
}

// MailboxQueue is the drain's read and claim of a thread's queued mail.
// store.AgentMessageExtStore satisfies it.
type MailboxQueue interface {
	ListQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) ([]*db.AgentMessage, error)
	MarkAgentMessagesDelivered(ctx context.Context, ids []string, deliveredAt time.Time, deliveredMessageID string) ([]string, error)
	SetAgentMessagesDeliveredMessageID(ctx context.Context, ids []string, deliveredMessageID string) error
}

// DrainAgentMessagesDeps is what a DrainAgentMessagesActivity is built from.
type DrainAgentMessagesDeps struct {
	// Store is what the drain reads and writes beside the mailbox.
	Store DrainAgentMessagesStore
	// Mailbox is the thread's queued mail.
	Mailbox MailboxQueue
	// Threads saves the envelope message.
	Threads MessageSaver
}

// MailboxReader peeks at whether a thread has queued mail.
// store.AgentMessageExtStore satisfies it.
type MailboxReader interface {
	ListQueuedAgentMessagesForThread(ctx context.Context, toThreadID string) ([]*db.AgentMessage, error)
}

// MailboxResolver marks a thread's stranded queued mail undelivered once its
// loop has exited. store.AgentMessageExtStore satisfies it.
type MailboxResolver interface {
	MarkQueuedAgentMessagesUndeliveredForThread(ctx context.Context, toThreadID string) (int64, error)
}

// ThreadStatusDeps is what a ThreadStatusActivity is built from.
type ThreadStatusDeps struct {
	// Store is what the activity writes.
	Store ThreadStatusStore
	// Mailbox resolves the mail a finished thread left behind.
	Mailbox MailboxResolver
}

// AttendanceTriggers is the trigger side of the attendance rule: the launch
// event of an agent.start_run chain, and the trigger that launched an
// unattended run (for its unattended-funding stamp). NewTriggerReads answers
// it. A nil one cannot decide, so the rule fails closed: unattended,
// org-funded sources only.
type AttendanceTriggers interface {
	GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error)
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
}

// LaunchEvents reads the launch event of a chat: what started a run decides
// whether its machine may be woken with the automation token.
type LaunchEvents interface {
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
}

// StatusTriggers is what a finished run's status write reads of the trigger
// that launched it: whether it opted in to completion notices, and the
// failure episode its firings form.
type StatusTriggers interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*core.TriggerEventWithRun, error)
}

// UserRefetcher tells a user's clients to re-read data of a given type (a
// user_updates refetch). chatupdates.Service satisfies it.
type UserRefetcher interface {
	EmitUserRefetch(ctx context.Context, userID string, refetchType core.RefetchType, opts core.RefetchOpts) error
}

// ChatRefetcher tells the clients watching a chat to re-read data of a given
// type (a chat_updates refetch). chatupdates.Service satisfies it.
type ChatRefetcher interface {
	EmitChatRefetch(ctx context.Context, chatID string, refetchType core.RefetchType) error
}
