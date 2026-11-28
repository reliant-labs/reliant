// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are each tool's own declaration of what it needs from
// the database; mock_gen.go mocks them. *db.Repo satisfies every one, and
// ToolsFactory hands it to each tool as that tool's role (store, factory.go).
//
// A tool's store lists the methods the tool calls itself and embeds the role
// of every helper or package it hands the store to, so when a callee needs
// more, the compiler points here.
package tools

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/planning"
	repopkg "github.com/reliant-labs/reliant/internal/repo"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
)

// --- helper roles ---------------------------------------------------------

// runCallerStore is what resolveRunCaller and resolveOwnedRun read: the
// calling chat and the run being managed. It is the whole store of the
// run-management tools that read nothing else (control_run, send_to_run,
// list_triggers).
type runCallerStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// threadReader is the thread read the plan walk makes up the spawn tree with.
type threadReader interface {
	GetThread(ctx context.Context, id string) (*db.Thread, error)
}

// planByThread is the plan read the tools make by thread.
// planning.Service satisfies it.
type planByThread interface {
	GetPlanByThreadID(ctx context.Context, threadID string) (*db.Plan, error)
}

// createPlanPlanning is create_plan's planning role.
type createPlanPlanning interface {
	planByThread
	CreateTaskDependency(ctx context.Context, dep *db.TaskDependency) error
}

// addTaskPlanning is add_task's planning role.
type addTaskPlanning interface {
	planByThread
}

// updatePlanPlanning is update_plan's planning role.
type updatePlanPlanning interface {
	planByThread
	UpdatePlan(ctx context.Context, plan *db.Plan) error
}

// getPlanPlanning is get_plan's planning role.
type getPlanPlanning interface {
	planByThread
	GetTaskStatsByPlan(ctx context.Context, planID string) (*planning.TaskStats, error)
}

// listTasksPlanning is list_tasks' planning role.
type listTasksPlanning interface {
	planByThread
	GetTaskStatsByPlan(ctx context.Context, planID string) (*planning.TaskStats, error)
	ListDependenciesByPlan(ctx context.Context, planID string) ([]*db.TaskDependency, error)
}

// updateTaskPlanning is update_task's planning role.
type updateTaskPlanning interface {
	planByThread
	GetTaskByPosition(ctx context.Context, planID string, position int) (*db.Task, error)
	GetTaskStatsByPlan(ctx context.Context, planID string) (*planning.TaskStats, error)
	UpdateTask(ctx context.Context, task *db.Task) error
}

// addDependencyPlanning is add_dependency's planning role.
type addDependencyPlanning interface {
	planByThread
	CreateTaskDependency(ctx context.Context, dep *db.TaskDependency) error
	GetTaskByPosition(ctx context.Context, planID string, position int) (*db.Task, error)
}

// removeDependencyPlanning is remove_dependency's planning role.
type removeDependencyPlanning interface {
	DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error
}

// listReadyTasksPlanning is list_ready_tasks' planning role.
type listReadyTasksPlanning interface {
	planByThread
	ListDependenciesByPlan(ctx context.Context, planID string) ([]*db.TaskDependency, error)
}

// workflowDraftLookup is what resolveWorkflowDraft reads to find a draft by
// id, slug or name.
type workflowDraftLookup interface {
	GetWorkflowDraft(ctx context.Context, id string) (*db.WorkflowDraft, error)
	GetWorkflowDraftByName(ctx context.Context, userID, name string) (*db.WorkflowDraft, error)
	GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
}

// inheritedSpawnLookup is what findInheritedSpawnChild reads: the sub-agents
// a thread inherited through a branch.
type inheritedSpawnLookup interface {
	ListInheritedSpawnChildren(ctx context.Context, threadID string) ([]*core.InheritedSpawnChild, error)
}

// spawnChildLister is what verifyIsOwnChild and spawnStopRefForChild read: a
// thread's own spawned children.
type spawnChildLister interface {
	ListSpawnChildren(ctx context.Context, threadID string) ([]*core.SpawnChild, error)
}

// spawnRelationStore is what spawnSendRelationship reads, beside the spawn
// tree, to place a target thread relative to the caller.
type spawnRelationStore interface {
	GetThread(ctx context.Context, id string) (*db.Thread, error)
}

// SpawnTree reads who spawned whom: a thread's own spawned children and the
// sub-agents it inherited by being a branch. store.ToolCallExtStore satisfies
// it. The spawn tools take it beside their store, and none of its reads is
// scoped to a caller: each tool passes its own thread id.
type SpawnTree interface {
	ListSpawnChildren(ctx context.Context, threadID string) ([]*core.SpawnChild, error)
	ListInheritedSpawnChildren(ctx context.Context, threadID string) ([]*core.InheritedSpawnChild, error)
}

// scenarioLoaderStore is what createScenarioWorkflowLoader reads: the caller's
// chat (for its project) and the workflow sources refs resolve against.
type scenarioLoaderStore interface {
	workflowsource.ProjectStore
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// The *Drafts roles are what each workflow and scenario tool reads and writes
// of the caller's saved workflows. workflowdrafts.Service satisfies every one:
// each read is scoped to what the caller may see and each write runs under the
// owner's org lock.

// listWorkflowsDrafts is list_workflows' drafts.
type listWorkflowsDrafts interface {
	workflowsource.DraftReads
	ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*db.WorkflowDraft, error)
}

// getWorkflowDrafts is get_workflow's drafts.
type getWorkflowDrafts interface {
	workflowsource.DraftReads
	workflowDraftLookup
}

// createWorkflowDrafts is create_workflow's drafts.
type createWorkflowDrafts interface {
	workflowsource.DraftReads
	CreateWorkflowDraft(ctx context.Context, draft *db.WorkflowDraft) error
}

// writeWorkflowDrafts is the drafts of edit_workflow and write_workflow.
type writeWorkflowDrafts interface {
	workflowsource.DraftReads
	workflowDraftLookup
	UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, status db.WorkflowDraftStatus) error
	// UpdateWorkflowDraftDefinitionAtVersion writes only while the draft is
	// still at version; otherwise workflowdrafts.ErrVersionConflict.
	UpdateWorkflowDraftDefinitionAtVersion(ctx context.Context, id string, version int64, name string, slug string, definition string, status db.WorkflowDraftStatus) error
}

// WorkflowDrafts is every read and write the workflow and scenario tools make
// of the caller's saved workflows (workflowdrafts.Service). Optional on
// ToolsOptions: nil makes those tools report that they are unavailable here,
// like a nil Repo.
type WorkflowDrafts interface {
	listWorkflowsDrafts
	getWorkflowDrafts
	createWorkflowDrafts
	writeWorkflowDrafts
	scenarioRunDrafts
}

// scenarioRunDrafts is the drafts of the tools that run a scenario: the draft
// it belongs to, and the workflows its refs resolve against.
type scenarioRunDrafts interface {
	workflowsource.DraftReads
	workflowDraftLookup
}

// ScenarioStore is the scenario rows the scenario tools find and rewrite.
// store.WorkflowScenarioExtStore satisfies it.
type ScenarioStore interface {
	FindWorkflowScenarioByName(ctx context.Context, draftID string, name string) (*db.WorkflowScenario, error)
	ReplaceWorkflowScenario(ctx context.Context, s *db.WorkflowScenario) error
}

// scenarioFinder is the scenario read of view_scenario, delete_scenario and
// run_scenario.
type scenarioFinder interface {
	FindWorkflowScenarioByName(ctx context.Context, draftID string, name string) (*db.WorkflowScenario, error)
}

// LaunchedRunCounter counts the runs a user's agents have launched and that
// are still live. store.TriggerEventExtStore satisfies it.
type LaunchedRunCounter interface {
	CountLiveLaunchedRuns(ctx context.Context, userID string, kind core.TriggerEventKind) (int, error)
	// GetTriggerEventByChat is the launch event of the given kind that started
	// chatID: what agentStartRunDepth walks up a run's lineage.
	GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error)
}

// WorktreeChatLister lists the chats bound to a worktree.
// store.ChatExtStore satisfies it.
type WorktreeChatLister interface {
	ListChatsForWorktree(ctx context.Context, userID, worktreeID string, limit int) ([]db.WorktreeChat, error)
}

// WorkflowSlugChecker reports whether a user already has a workflow with a
// slug. store.WorkflowDraftExtStore satisfies it.
type WorkflowSlugChecker interface {
	WorkflowSlugExists(ctx context.Context, userID string, slug string) (bool, error)
}

// scenarioExecStore is what runScenarioByName writes and loads with: the
// scenario's recorded result and the loader the run resolves refs with.
type scenarioExecStore interface {
	scenarioLoaderStore
	UpdateWorkflowScenarioResult(ctx context.Context, id string, status string, result string) error
}

// --- planning and task tools ---------------------------------------------

// createPlanStore is create_plan's store.
type createPlanStore interface {
	CreatePlan(ctx context.Context, plan *db.Plan) error
	CreateTask(ctx context.Context, task *db.Task) error
}

// updatePlanStore is update_plan's store.
type updatePlanStore interface {
	threadReader
}

// getPlanStore is get_plan's store.
type getPlanStore interface {
	threadReader
}

// addTaskStore is add_task's store.
type addTaskStore interface {
	threadReader
	CreateTask(ctx context.Context, task *db.Task) error
}

// listTasksStore is list_tasks' store.
type listTasksStore interface {
	threadReader
	ListTasksByPlan(ctx context.Context, planID string) ([]*db.Task, error)
}

// taskScopeStore is what scopeTaskForThread reads to place a task's plan
// relative to the calling thread.
type taskScopeStore interface {
	GetPlan(ctx context.Context, id string) (*db.Plan, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
}

// updateTaskStore is update_task's store.
type updateTaskStore interface {
	threadReader
	GetPlan(ctx context.Context, id string) (*db.Plan, error)
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	GetTask(ctx context.Context, id string) (*db.Task, error)
}

// createSubtaskStore is create_subtask's store.
type createSubtaskStore interface {
	taskScopeStore
	CreateTask(ctx context.Context, task *db.Task) error
	GetTask(ctx context.Context, id string) (*db.Task, error)
}

// addDependencyStore is add_dependency's store.
type addDependencyStore interface {
	GetTask(ctx context.Context, id string) (*db.Task, error)
}

// removeDependencyStore is remove_dependency's store.
type removeDependencyStore interface {
}

// listReadyTasksStore is list_ready_tasks' store.
type listReadyTasksStore interface {
	threadReader
	ListTasksByPlan(ctx context.Context, planID string) ([]*db.Task, error)
}

// --- attachment and media tools -------------------------------------------

// readAttachmentStore is read_attachment's store.
type readAttachmentStore interface {
	GetAttachment(ctx context.Context, id string) (*db.Attachment, error)
}

// saveAttachmentStore is save_attachment's store.
type saveAttachmentStore interface {
	GetAttachment(ctx context.Context, id string) (*db.Attachment, error)
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// presentImageStore is present_image's store.
type presentImageStore interface {
	CreateAttachment(ctx context.Context, attachment *db.Attachment) error
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// generateImageStore is generate_image's store.
type generateImageStore interface {
	CreateAttachment(ctx context.Context, attachment *db.Attachment) error
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// generateVideoStore is generate_video's store.
type generateVideoStore interface {
	CreateAttachment(ctx context.Context, attachment *db.Attachment) error
	GetAttachment(ctx context.Context, id string) (*db.Attachment, error)
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// --- spawn tools ------------------------------------------------------------

// spawnStatusStore is spawn_status's store. It is handed to threads.NewService
// for the child's last assistant message.
type spawnStatusStore interface {
	threads.Repository
	CountMessagesInThread(ctx context.Context, threadID string) (int, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	LastThreadActivityByChat(ctx context.Context, chatID string) (map[string]time.Time, error)
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error)
}

// providerBackoffReader reads a chat's provider backoff markers.
// providerbackoff.Service satisfies it.
type providerBackoffReader interface {
	ProviderBackoffByChat(ctx context.Context, chatID string) (map[string]core.ProviderBackoff, error)
}

// SpawnQuestions is what spawn_status reads to say which agents are waiting on
// the user: each thread's newest pending blocking question. store.QuestionExtStore
// satisfies it.
type SpawnQuestions interface {
	PendingQuestionsByThread(ctx context.Context, chatID string) (map[string]*db.Question, error)
}

// SpawnStatusDeps is what the spawn_status tool is built from.
type SpawnStatusDeps struct {
	// Store reads the spawn tree, signals and messages.
	Store spawnStatusStore
	// Questions reads the pending questions agents are parked on.
	Questions SpawnQuestions
	// Tree reads the caller's own and inherited sub-agents.
	Tree SpawnTree
	// Backoff reads the provider backoff markers agents are parked on.
	Backoff providerBackoffReader
}

// SpawnSendDeps is what the spawn_send tool is built from.
type SpawnSendDeps struct {
	// Store reads threads.
	Store spawnSendStore
	// Mailbox queues the message. Nil makes the tool report itself
	// unavailable.
	Mailbox SpawnMailbox
	// Tree places the target relative to the caller.
	Tree SpawnTree
	// Notifier rings the recipient's doorbell; nil is no doorbell.
	Notifier AgentMessageNotifier
}

// SpawnStopDeps is what the spawn_stop tool is built from.
type SpawnStopDeps struct {
	// Store reads the target thread.
	Store spawnStopStore
	// Tree resolves the caller's own children.
	Tree SpawnTree
	// Stopper requests the stop; nil is no way to stop anything.
	Stopper SpawnStopper
}

// spawnSendStore is spawn_send's store.
type spawnSendStore interface {
	spawnRelationStore
}

// SpawnMailbox queues spawn_send's message into the recipient's mailbox and
// announces it to the chat's clients. *agentmailbox.Mailbox satisfies it.
type SpawnMailbox interface {
	EnqueueAgentMessage(ctx context.Context, msg *db.AgentMessage) error
}

// spawnStopStore is spawn_stop's store.
type spawnStopStore interface {
	GetThread(ctx context.Context, id string) (*db.Thread, error)
}

// --- run tools --------------------------------------------------------------

// startRunStore is start_run's store.
type startRunStore interface {
	runCallerStore
	GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error)
}

// listRunsStore is list_runs' store.
type listRunsStore interface {
	runCallerStore
	ListRuns(ctx context.Context, filters db.RunListFilters) ([]*db.RunListItem, bool, error)
}

// getRunStore is get_run's store. It is handed to threads.NewService for the
// run's last assistant message.
type getRunStore interface {
	threads.Repository
	runCallerStore
	ListRunNodeExecutions(ctx context.Context, workflowID string, limit int) ([]*db.StepExecution, error)
}

// RunWorkflows reads a run's root workflow, which carries why it failed
// (get_run). store.WorkflowExtStore satisfies it.
type RunWorkflows interface {
	LoadWorkflow(ctx context.Context, id string) (*db.Workflow, error)
}

// PlacementRecords reads the worktree, project and machine rows the worktree
// and report_bug tools name. daemonplacement.Records satisfies it. It checks
// no owner: each tool reads only the rows of the run it serves.
type PlacementRecords interface {
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// bugReportMachines is what report_bug reads about the machine a report came
// from.
type bugReportMachines interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// worktreeRecords is what the worktree tool reads besides its store: the
// chat's project and a worktree row it archived.
type worktreeRecords interface {
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
}

// bugReportLookup is what report_bug reads to say where a report came from.
type bugReportLookup interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetLatestMessageInThread(ctx context.Context, threadID string) (*db.Message, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	// ListFreshDaemonAttachmentsForUser names the build each of the user's
	// attached daemons registered with: the daemon's own version, which is
	// not the server's.
	ListFreshDaemonAttachmentsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]*db.DaemonAttachment, error)
}

// --- workflow tools ---------------------------------------------------------

// listWorkflowsStore is list_workflows' store. Validation and the project
// index read through workflowsource.
type listWorkflowsStore interface {
	workflowsource.ProjectStore
}

// getWorkflowStore is get_workflow's store.
type getWorkflowStore interface {
	workflowsource.ProjectStore
}

// createWorkflowStore is create_workflow's store.
type createWorkflowStore interface {
	workflowsource.ProjectStore
}

// workflowWriteStore is the store of edit_workflow and write_workflow, which
// both resolve a draft, validate the new definition and save it.
type workflowWriteStore interface {
	workflowsource.ProjectStore
}

// --- scenario tools ---------------------------------------------------------

// listScenariosStore is list_scenarios' store.
type listScenariosStore interface {
	ListWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) ([]*db.WorkflowScenario, error)
}

// viewScenarioStore is view_scenario's store.
type viewScenarioStore interface {
}

// editScenarioStore is edit_scenario's store.
type editScenarioStore interface {
}

// writeScenarioStore is write_scenario's store; it runs the scenario it saves.
type writeScenarioStore interface {
	scenarioExecStore
	CreateWorkflowScenario(ctx context.Context, scenario *db.WorkflowScenario) error
}

// deleteScenarioStore is delete_scenario's store.
type deleteScenarioStore interface {
	DeleteWorkflowScenario(ctx context.Context, id string) error
}

// runScenarioStore is run_scenario's store.
type runScenarioStore interface {
	scenarioExecStore
}

// --- worktree tool ----------------------------------------------------------

// worktreeStore is the worktree tool's store. Creating a worktree goes
// through workspacecreate, and a project's repos are adopted from the machine
// through repo.AdoptFromDaemon.
type worktreeStore interface {
	workspacecreate.Store
	repopkg.AdoptStore
	GetLiveChatIDForWorktree(ctx context.Context, worktreeID string) (string, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
}

// listPendingQuestionsStore is list_pending_questions' store: the open
// questions in the caller's thread subtree.
type listPendingQuestionsStore interface {
	ListPendingQuestionsInThreadSubtree(ctx context.Context, chatID, threadID string) ([]db.SubtreeQuestion, error)
}

// withdrawQuestionStore is withdraw_question's store.
type withdrawQuestionStore interface {
	WithdrawAsyncQuestion(ctx context.Context, chatID, threadID, questionID, reason string) (db.WithdrawOutcome, error)
}

// Refetcher tells a chat's or a user's clients to re-read what a tool changed
// (a plan, its tasks, the worktree list). chatupdates.Service satisfies it. A
// nil Refetcher tells nobody: the row is the source of truth and clients keep
// a fallback poll, so a missing announcement degrades latency, not correctness.
type Refetcher interface {
	EmitChatRefetch(ctx context.Context, chatID string, refetchType core.RefetchType) error
	EmitUserRefetch(ctx context.Context, userID string, refetchType core.RefetchType, opts core.RefetchOpts) error
}

// emitChatRefetch announces a chat refetch through r, nil-safe: the tools run
// without a Refetcher in the daemon runtime and in tests.
func emitChatRefetch(r Refetcher, ctx context.Context, chatID string, t core.RefetchType) error {
	if r == nil {
		return nil
	}
	return r.EmitChatRefetch(ctx, chatID, t)
}
