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

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
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

// planLookupStore is what resolvePlanForRead and resolvePlanForWrite read to
// find the plan a thread works on.
type planLookupStore interface {
	GetPlanByThreadID(ctx context.Context, threadID string) (*db.Plan, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
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
	ListInheritedSpawnChildren(ctx context.Context, threadID string) ([]*db.InheritedSpawnChild, error)
}

// spawnChildLister is what verifyIsOwnChild and spawnStopRefForChild read: a
// thread's own spawned children.
type spawnChildLister interface {
	ListSpawnChildren(ctx context.Context, threadID string) ([]*db.SpawnChild, error)
}

// spawnRelationStore is what spawnSendRelationship reads to place a target
// thread relative to the caller.
type spawnRelationStore interface {
	spawnChildLister
	GetThread(ctx context.Context, id string) (*db.Thread, error)
}

// startRunLineageReader is what agentStartRunDepth walks: the launch event
// that started each run in the caller's lineage.
type startRunLineageReader interface {
	GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error)
}

// scenarioLoaderStore is what createScenarioWorkflowLoader reads: the caller's
// chat (for its project) and the workflow sources refs resolve against.
type scenarioLoaderStore interface {
	workflowsource.Store
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// scenarioExecStore is what runScenarioByName reads and writes: the scenario,
// its recorded result, and the loader the run resolves refs with.
type scenarioExecStore interface {
	scenarioLoaderStore
	GetWorkflowScenarioByName(ctx context.Context, workflowDraftID string, name string) (*db.WorkflowScenario, error)
	UpdateWorkflowScenarioResult(ctx context.Context, id string, status string, result string) error
}

// --- planning and task tools ---------------------------------------------

// createPlanStore is create_plan's store.
type createPlanStore interface {
	CreatePlan(ctx context.Context, plan *db.Plan) error
	CreateTask(ctx context.Context, task *db.Task) error
	CreateTaskDependency(ctx context.Context, dep *db.TaskDependency) error
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
	GetPlanByThreadID(ctx context.Context, threadID string) (*db.Plan, error)
}

// updatePlanStore is update_plan's store.
type updatePlanStore interface {
	planLookupStore
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
	UpdatePlan(ctx context.Context, plan *db.Plan) error
}

// getPlanStore is get_plan's store.
type getPlanStore interface {
	planLookupStore
	GetTaskStatsByPlan(ctx context.Context, planID string) (*db.TaskStats, error)
}

// addTaskStore is add_task's store.
type addTaskStore interface {
	planLookupStore
	CreateTask(ctx context.Context, task *db.Task) error
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
}

// listTasksStore is list_tasks' store.
type listTasksStore interface {
	planLookupStore
	GetTaskStatsByPlan(ctx context.Context, planID string) (*db.TaskStats, error)
	ListDependenciesByPlan(ctx context.Context, planID string) ([]*db.TaskDependency, error)
	ListTasksByPlan(ctx context.Context, planID string) ([]*db.Task, error)
}

// updateTaskStore is update_task's store.
type updateTaskStore interface {
	planLookupStore
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
	GetTask(ctx context.Context, id string) (*db.Task, error)
	GetTaskByPosition(ctx context.Context, planID string, position int) (*db.Task, error)
	GetTaskStatsByPlan(ctx context.Context, planID string) (*db.TaskStats, error)
	UpdateTask(ctx context.Context, task *db.Task) error
}

// createSubtaskStore is create_subtask's store.
type createSubtaskStore interface {
	CreateTask(ctx context.Context, task *db.Task) error
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
	GetTask(ctx context.Context, id string) (*db.Task, error)
}

// addDependencyStore is add_dependency's store.
type addDependencyStore interface {
	CreateTaskDependency(ctx context.Context, dep *db.TaskDependency) error
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
	GetPlanByThreadID(ctx context.Context, threadID string) (*db.Plan, error)
	GetTask(ctx context.Context, id string) (*db.Task, error)
	GetTaskByPosition(ctx context.Context, planID string, position int) (*db.Task, error)
}

// removeDependencyStore is remove_dependency's store.
type removeDependencyStore interface {
	DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error
	EmitChatRefetch(ctx context.Context, chatID string, refetchType db.RefetchType) error
}

// listReadyTasksStore is list_ready_tasks' store.
type listReadyTasksStore interface {
	planLookupStore
	ListDependenciesByPlan(ctx context.Context, planID string) ([]*db.TaskDependency, error)
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
	spawnChildLister
	inheritedSpawnLookup
	CountMessagesInThread(ctx context.Context, threadID string) (int, error)
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	LastThreadActivityByChat(ctx context.Context, chatID string) (map[string]time.Time, error)
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error)
	PendingQuestionsByThread(ctx context.Context, chatID string) (map[string]*db.Question, error)
	ProviderBackoffByChat(ctx context.Context, chatID string) (map[string]db.ProviderBackoff, error)
}

// spawnSendStore is spawn_send's store.
type spawnSendStore interface {
	spawnRelationStore
	inheritedSpawnLookup
	EnqueueAgentMessage(ctx context.Context, msg *db.AgentMessage) error
}

// spawnStopStore is spawn_stop's store.
type spawnStopStore interface {
	spawnChildLister
	inheritedSpawnLookup
	GetThread(ctx context.Context, id string) (*db.Thread, error)
}

// --- run tools --------------------------------------------------------------

// startRunStore is start_run's store.
type startRunStore interface {
	runCallerStore
	startRunLineageReader
	CountLiveLaunchedRuns(ctx context.Context, userID string, kind core.TriggerEventKind) (int, error)
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
	// GetWorkflow reads the run's root workflow, which carries why it failed.
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
}

// bugReportLookup is what report_bug reads to say where a report came from.
type bugReportLookup interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetLatestMessageInThread(ctx context.Context, threadID string) (*db.Message, error)
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
}

// --- workflow tools ---------------------------------------------------------

// listWorkflowsStore is list_workflows' store. Validation and the project
// index read through workflowsource.
type listWorkflowsStore interface {
	workflowsource.Store
	ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*db.WorkflowDraft, error)
}

// getWorkflowStore is get_workflow's store.
type getWorkflowStore interface {
	workflowsource.Store
	workflowDraftLookup
}

// createWorkflowStore is create_workflow's store.
type createWorkflowStore interface {
	workflowsource.Store
	CreateWorkflowDraft(ctx context.Context, draft *db.WorkflowDraft) error
	WorkflowSlugExists(ctx context.Context, userID, slug string) (bool, error)
}

// workflowWriteStore is the store of edit_workflow and write_workflow, which
// both resolve a draft, validate the new definition and save it.
type workflowWriteStore interface {
	workflowsource.Store
	workflowDraftLookup
	UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, status db.WorkflowDraftStatus) error
}

// --- scenario tools ---------------------------------------------------------

// listScenariosStore is list_scenarios' store.
type listScenariosStore interface {
	workflowDraftLookup
	ListWorkflowScenariosByDraft(ctx context.Context, workflowDraftID string) ([]*db.WorkflowScenario, error)
}

// viewScenarioStore is view_scenario's store.
type viewScenarioStore interface {
	workflowDraftLookup
	GetWorkflowScenarioByName(ctx context.Context, workflowDraftID string, name string) (*db.WorkflowScenario, error)
}

// editScenarioStore is edit_scenario's store.
type editScenarioStore interface {
	workflowDraftLookup
	GetWorkflowScenarioByName(ctx context.Context, workflowDraftID string, name string) (*db.WorkflowScenario, error)
	UpdateWorkflowScenario(ctx context.Context, scenario *db.WorkflowScenario) error
}

// writeScenarioStore is write_scenario's store; it runs the scenario it saves.
type writeScenarioStore interface {
	workflowDraftLookup
	scenarioExecStore
	CreateWorkflowScenario(ctx context.Context, scenario *db.WorkflowScenario) error
	UpdateWorkflowScenario(ctx context.Context, scenario *db.WorkflowScenario) error
}

// deleteScenarioStore is delete_scenario's store.
type deleteScenarioStore interface {
	workflowDraftLookup
	DeleteWorkflowScenario(ctx context.Context, id string) error
	GetWorkflowScenarioByName(ctx context.Context, workflowDraftID string, name string) (*db.WorkflowScenario, error)
}

// runScenarioStore is run_scenario's store.
type runScenarioStore interface {
	workflowDraftLookup
	scenarioExecStore
}

// --- worktree tool ----------------------------------------------------------

// worktreeStore is the worktree tool's store. Creating a worktree goes
// through workspacecreate, and a project's repos are adopted from the machine
// through repo.AdoptFromDaemon.
type worktreeStore interface {
	workspacecreate.Store
	repopkg.AdoptStore
	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error
	GetLiveChatIDForWorktree(ctx context.Context, worktreeID string) (string, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
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
