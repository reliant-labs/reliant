// Copyright (c) 2025 Reliant Labs
package activities

import (
	"reflect"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/approvalclose"
	"github.com/reliant-labs/reliant/internal/chatupdates"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/questions"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// nodeTypeActivityDef maps a node type to its Temporal activity name and Go types.
// Only non-structural node types with corresponding Temporal activities are listed.
type nodeTypeActivityDef struct {
	activityName string
	inputType    reflect.Type
	outputType   reflect.Type
}

var nodeTypeActivities = map[string]nodeTypeActivityDef{
	model.NodeTypeCallLLM:        {"CallLLM", reflect.TypeOf(reliantv1.CallLLMArgs{}), reflect.TypeOf((*reliantv1.CallLLMOutput)(nil))},
	model.NodeTypeExecuteTools:   {"ExecuteTools", reflect.TypeOf(reliantv1.ExecuteToolsArgs{}), reflect.TypeOf((*reliantv1.ExecuteToolsOutput)(nil))},
	model.NodeTypeCompact:        {"Compact", reflect.TypeOf(reliantv1.CompactArgs{}), reflect.TypeOf(handlers.CompactOutput{})},
	model.NodeTypeCreateWorktree: {"CreateWorktree", reflect.TypeOf(reliantv1.CreateWorktreeArgs{}), reflect.TypeOf(handlers.CreateWorktreeOutput{})},
	model.NodeTypeAskQuestion:    {"AskQuestion", reflect.TypeOf(reliantv1.AskQuestionArgs{}), reflect.TypeOf((*reliantv1.AskQuestionOutput)(nil))},
	model.NodeTypeSaveMessage:    {"SaveMessage", reflect.TypeOf(reliantv1.SaveMessageNodeArgs{}), reflect.TypeOf((*reliantv1.SaveMessageOutput)(nil))},
	model.NodeTypeInvokeTool:     {"InvokeTool", reflect.TypeOf(reliantv1.InvokeToolArgs{}), reflect.TypeOf((*reliantv1.InvokeToolOutput)(nil))},
	model.NodeTypeAction:         {"Action", reflect.TypeOf(reliantv1.ActionArgs{}), reflect.TypeOf((*reliantv1.ActionOutput)(nil))},
	model.NodeTypeGitPublish:     {"GitPublish", reflect.TypeOf(reliantv1.GitPublishArgs{}), reflect.TypeOf((*reliantv1.GitPublishOutput)(nil))},
}

func init() {
	// Set up the PreflightConfig for RequiresDaemon static analysis.
	// This bridges the tools package (which knows tool locations) with the
	// runtime package (which can't import tools due to import cycles).
	initPreflightConfig()

	// Auto-register node-type activities from NodeMeta proto annotations.
	// Proto descriptors and metadata (display_name, description, category) are
	// discovered from annotations, replacing per-activity boilerplate.
	registerNodeTypeActivities()

	// ExecuteRunStep (not visible in builder, no metadata registration)
	schema.RegisterActivityType("ExecuteRunStep",
		reflect.TypeOf(handlers.ExecuteRunStepInput{}),
		reflect.TypeOf(handlers.ExecuteRunStepOutput{}))

	// DeleteWorktree (utility activity, no node type — not visible in builder)
	schema.RegisterActivityType("DeleteWorktree",
		reflect.TypeOf(handlers.DeleteWorktreeInput{}),
		reflect.TypeOf(handlers.DeleteWorktreeOutput{}))

	// ApprovalCreate (not visible in builder, no metadata registration)
	schema.RegisterActivityType("ApprovalCreate",
		reflect.TypeOf(handlers.ApprovalCreateInput{}),
		reflect.TypeOf(handlers.ApprovalCreateOutput{}))

	// ApprovalResolve (not visible in builder, no metadata registration)
	schema.RegisterActivityType("ApprovalResolve",
		reflect.TypeOf(handlers.ApprovalResolveInput{}),
		reflect.TypeOf(handlers.ApprovalResolveOutput{}))

	// QuestionCreate (not visible in builder, no metadata registration)
	schema.RegisterActivityType("QuestionCreate",
		reflect.TypeOf(handlers.QuestionCreateInput{}),
		reflect.TypeOf(handlers.QuestionCreateOutput{}))

	// QuestionResolve (not visible in builder, no metadata registration)
	schema.RegisterActivityType("QuestionResolve",
		reflect.TypeOf(handlers.QuestionResolveInput{}),
		reflect.TypeOf(handlers.QuestionResolveOutput{}))

	// CreateWorkflowWithThread (not visible in builder, no metadata registration)
	schema.RegisterActivityType("CreateWorkflowWithThread",
		reflect.TypeOf(handlers.CreateWorkflowWithThreadInput{}),
		reflect.TypeOf(handlers.CreateWorkflowWithThreadOutput{}))

}

// registerNodeTypeActivities uses DiscoverNodeMetas to auto-register activity types,
// proto descriptors, and metadata for non-structural node types.
func registerNodeTypeActivities() {
	metas := model.DiscoverNodeMetas()

	// Build a map of node_type -> args message descriptor from the V2Node.args oneof.
	argsDescs := make(map[string]protoreflect.MessageDescriptor)
	nodeDesc := (&reliantv1.Node{}).ProtoReflect().Descriptor()
	argsOneof := nodeDesc.Oneofs().ByName("args")
	if argsOneof != nil {
		fields := argsOneof.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			msgDesc := fd.Message()
			if msgDesc == nil {
				continue
			}
			opts := msgDesc.Options()
			if opts == nil {
				continue
			}
			ext := proto.GetExtension(opts, reliantv1.E_NodeMeta)
			if ext == nil {
				continue
			}
			meta, ok := ext.(*reliantv1.NodeMeta)
			if !ok || meta == nil || meta.NodeType == "" {
				continue
			}
			argsDescs[meta.NodeType] = msgDesc
		}
	}

	// Use TypeRegistry to look up output descriptors for each node type.
	typeRegistry := wfcel.NewTypeRegistry()

	// Structural node types that should appear in the workflow builder sidebar.
	// These don't have separate Temporal activities but need catalog metadata.
	visibleStructuralNodes := map[string]bool{
		model.NodeTypeRun:      true,
		model.NodeTypeWorkflow: true,
		model.NodeTypeLoop:     true,
		model.NodeTypeJoin:     true,
		model.NodeTypeRouter:   true,
		model.NodeTypeCompute:  true,
	}

	for nodeType, meta := range metas {
		def, ok := nodeTypeActivities[nodeType]
		if ok {
			// Non-structural activity node: register types and metadata.
			schema.RegisterActivityType(def.activityName, def.inputType, def.outputType)
			if msgDesc, ok := argsDescs[nodeType]; ok {
				outputDesc, _ := typeRegistry.OutputForNodeType(nodeType)
				schema.RegisterNodeTypeActivity(def.activityName, meta, msgDesc, outputDesc)
			}
		} else if visibleStructuralNodes[nodeType] {
			// Structural node visible in builder: register metadata only (no Temporal activity).
			if msgDesc, ok := argsDescs[nodeType]; ok {
				outputDesc, _ := typeRegistry.OutputForNodeType(nodeType)
				schema.RegisterNodeTypeActivity(meta.NodeType, meta, msgDesc, outputDesc)
			}
		}
	}
}

// RegisterAll registers all runtime workflow activities with the Temporal worker registry.
// Activity types and metadata are registered in init() for static analysis.
// This function registers activity instances with their runtime dependencies.
func RegisterAll(registry *v2.ActivityRegistry, deps *Activities) {
	var workflowRuns handlers.WorkflowRuns
	var toolCalls handlers.ToolCalls
	var questionsByStep handlers.QuestionsByStep
	var questionWriter handlers.QuestionWriter
	var workspaceRows handlers.WorkspaceRows
	// updates tells clients what the activities changed; it publishes through
	// the repository's update notifiers, as the repository's own writes do.
	var updates chatupdates.Service
	if deps.Repo != nil {
		updates = deps.Repo.UpdateWriter()
		workspaceRows = handlers.NewWorkspaceRows(deps.Repo.ORM())
		workflowRuns = handlers.NewWorkflowRuns(deps.Repo.ORM())
		toolCalls = handlers.NewToolCalls(deps.Repo.ORM())
		questionsByStep = store.NewQuestionExtStore(deps.Repo.ORM())
		questionWriter = questions.NewFromRepo(deps.Repo)
	}

	// ========================================================================
	// MESSAGE PROCESSING ACTIVITIES
	// ========================================================================

	saveMessage := handlers.NewSaveMessageActivity(handlers.SaveMessageDeps{Store: deps.Repo, Threads: deps.Threads})
	v2.RegisterActivity(registry, saveMessage)
	// A node's save_message is written by the worker that executes the node,
	// through the same write path as the SaveMessage activity.
	registry.SetMessageWriter(saveMessage)
	// A node's observe: block is recorded by the same worker, after its
	// save_message (a nil *db.Repo would be a non-nil interface that panics).
	if deps.Repo != nil {
		registry.SetObservationWriter(deps.Repo)
	}
	var modelEndpoints local.EndpointSource
	var spawnTree handlers.SpawnTree
	if deps.Repo != nil {
		modelEndpoints = store.NewModelEndpointExtStore(deps.Repo.ORM())
		spawnTree = store.NewToolCallExtStore(deps.Repo.ORM())
	}
	var stoppedAgents handlers.StoppedSubAgentReader
	// The mailbox: its queue (drain, late-arrival peek), its enqueue with the
	// announcement, and the writes that resolve or preempt on it. Each is the
	// one generated store or the one Mailbox, nil when there is no database.
	var mailboxStore *agentMessageStores
	if deps.Repo != nil {
		stoppedAgents = store.NewToolCallExtStore(deps.Repo.ORM())
		ams := store.NewAgentMessageExtStore(deps.Repo.ORM())
		mailboxStore = &agentMessageStores{
			queue:    ams,
			resolver: ams,
			preempt:  ams,
			enqueue:  agentmailbox.New(agentmailbox.Deps{Store: ams, Announcer: deps.Repo.UpdateWriter()}),
		}
	} else {
		mailboxStore = &agentMessageStores{}
	}
	// The projects attached to a chat, which its tools can run in (a nil
	// *db.Repo here would be a non-nil interface that panics).
	// The trigger reads of the attendance rule, the machine wake and the run
	// status write; nil without a database, which decides nothing (fail closed).
	var attendanceTriggers handlers.AttendanceTriggers
	var launchEvents handlers.LaunchEvents
	var statusTriggers handlers.StatusTriggers
	if deps.Repo != nil {
		reads := handlers.NewTriggerReads(deps.Repo.ORM())
		attendanceTriggers, launchEvents, statusTriggers = reads, reads, reads
	}
	var chatProjects handlers.ChatProjectLister
	if deps.Repo != nil {
		chatProjects = deps.Repo
	}
	v2.RegisterActivity(registry, handlers.NewCallLLMActivity(handlers.CallLLMDeps{Store: tools.WithChats(deps.Repo), Triggers: attendanceTriggers, Spawns: spawnTree, Threads: deps.Threads, ToolResults: toolCalls, Mailbox: mailboxStore.queue, Hub: deps.StreamingHub, ToolsFactory: deps.ToolsFactory, ConfigProvider: deps.ConfigProvider, DriverResolver: deps.DriverResolver, MCPBinder: deps.MCPBinder}).WithModelEndpoints(modelEndpoints).WithDaemonRouter(deps.DaemonRouter).WithPrivateEgress(deps.AllowPrivateEgress).WithAccountRouting(deps.AccountRouting).WithUsageRecorder(deps.LLMUsage).WithProviderBackoff(deps.ProviderBackoff).WithChatProjects(chatProjects).WithFeatureGate(deps.FeatureGate))

	// ========================================================================
	// TOOL EXECUTION ACTIVITIES
	// ========================================================================

	// The skill tool reads the project's skills through the same config
	// provider CallLLM offered them from (cached per config version on the
	// worker), not by re-reading the whole config row per call.
	var reviver handlers.WorktreeReviver
	var approvalsLookup handlers.ApprovalLookup
	var capacitySlots handlers.TriggerCapacitySlotStore
	if deps.Repo != nil {
		approvalsLookup = store.NewApprovalExtStore(deps.Repo.ORM())
		capacitySlots = handlers.NewTriggerCapacitySlots(deps.Repo.ORM())
	}
	if deps.Repo != nil {
		reviver = store.NewWorktreeExtStore(deps.Repo.ORM())
	}
	var runFunding handlers.RunFundingStore
	if deps.Repo != nil {
		runFunding = handlers.NewRunFundingStore(deps.Repo.ORM(), deps.Repo)
	}
	v2.RegisterActivity(registry, handlers.NewExecuteToolsActivityWithDeps(handlers.ExecuteToolsDeps{Store: tools.WithChats(deps.Repo), Triggers: attendanceTriggers, Rows: workspaceRows, ToolExecutor: deps.ToolExecutor, ToolCalls: toolCalls, Preemption: mailboxStore.preempt}).WithUpdates(userRefetcher(updates)).WithRunFunding(runFunding).WithToolUsage(deps.ToolUsage).WithWorktreeReviver(reviver).WithConfigProvider(deps.ConfigProvider).WithDaemonRouter(deps.DaemonRouter).WithChatProjects(chatProjects))
	v2.RegisterActivity(registry, handlers.NewInvokeToolActivityWithDeps(handlers.ExecuteToolsDeps{Store: tools.WithChats(deps.Repo), Triggers: attendanceTriggers, Rows: workspaceRows, ToolExecutor: deps.ToolExecutor, ToolCalls: toolCalls, Preemption: mailboxStore.preempt}).WithUpdates(userRefetcher(updates)).WithRunFunding(runFunding).WithToolUsage(deps.ToolUsage).WithWorktreeReviver(reviver).WithConfigProvider(deps.ConfigProvider).WithDaemonRouter(deps.DaemonRouter).WithChatProjects(chatProjects))
	v2.RegisterActivity(registry, handlers.NewActionActivity(deps.Connections).WithLedger(deps.WriteLedger).WithHostedExecutor(deps.HostedActions).WithSecretResolver(deps.SecretResolver).WithServerActions(deps.ServerActions))

	// ========================================================================
	// CONTEXT MANAGEMENT ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewCompactActivity(handlers.CompactDeps{Store: tools.WithChats(deps.Repo), Triggers: attendanceTriggers, Threads: deps.Threads, ToolResults: toolCalls, DriverResolver: deps.DriverResolver}).WithAccountRouting(deps.AccountRouting).WithUsageRecorder(deps.LLMUsage))
	v2.RegisterActivity(registry, handlers.NewGenerateTitleActivity(tools.WithChats(deps.Repo), attendanceTriggers, deps.DriverResolver).WithAccountRouting(deps.AccountRouting).WithUsageRecorder(deps.LLMUsage).WithProviderPreferences(deps.Repo))
	v2.RegisterActivity(registry, handlers.NewDraftAutomationActivity(deps.DriverResolver).WithAccountRouting(deps.AccountRouting).WithUsageRecorder(deps.LLMUsage).WithProviderPreferences(deps.Repo))

	// ========================================================================
	// APPROVAL ACTIVITIES
	// ========================================================================

	var approvals handlers.ApprovalResolveStore
	var runEvents runevents.Store
	if deps.Repo != nil {
		approvals = approvalclose.NewFromRepo(deps.Repo)
		runEvents = runevents.NewStore(deps.Repo.ORM())
	}

	v2.RegisterActivity(registry, handlers.NewApprovalCreateActivity(handlers.ApprovalCreateDeps{Repo: deps.Repo, Approvals: approvalsLookup, RunEvents: runEvents}))
	v2.RegisterActivity(registry, handlers.NewAutomationApprovalActivity(deps.ToolsFactory).WithExecutionStore(tools.WithChats(deps.Repo), workspaceRows))
	v2.RegisterActivity(registry, handlers.NewApprovalResolveActivity(approvals))

	// ========================================================================
	// QUESTION ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewQuestionCreateActivity(deps.Repo, questionWriter, questionsByStep, runEvents).WithUpdates(userRefetcher(updates)))
	v2.RegisterActivity(registry, handlers.NewQuestionResolveActivity(deps.Repo, questionWriter))

	// ========================================================================
	// RUN STEP ACTIVITIES
	// ========================================================================

	runStep := handlers.NewExecuteRunStepActivity(deps.Repo, workspaceRows, deps.ToolExecutor, deps.RunExecutor).WithDaemonRouter(deps.DaemonRouter)
	if deps.DaemonRouter != nil {
		// A command that never reached its machine watches the machine while
		// it waits, as wait_for_machine does.
		runStep = runStep.WithMachineObserver(toolexec.NewRunMachine(deps.DaemonRouter))
	}
	v2.RegisterActivity(registry, runStep)

	// ========================================================================
	// WORKTREE ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewCreateWorktreeActivity(deps.Repo, workspaceRows, deps.DaemonRouter).WithUpdates(userRefetcher(updates)))
	v2.RegisterActivity(registry, handlers.NewGitPublishActivity(tools.WithChats(deps.Repo), workspaceRows, deps.DaemonRouter, deps.Connections).WithLedger(deps.WriteLedger))
	v2.RegisterActivity(registry, handlers.NewDeleteWorktreeActivity(deps.Repo, workspaceRows, deps.DaemonRouter))

	// ========================================================================
	// WORKFLOW MANAGEMENT ACTIVITIES
	// ========================================================================

	var runRecords handlers.RunRecords
	var attention handlers.AttentionRecorder
	var pauser handlers.TriggerPauser
	var checkpoints handlers.WorkflowCheckpointStore
	if deps.Repo != nil {
		runRecords = handlers.NewRunRecords(deps.Repo.ORM())
		attention = handlers.NewAttentionRecorder(deps.Repo.ORM())
		pauser = store.NewTriggerWritesExtStore(deps.Repo.ORM())
		checkpoints = store.NewWorkflowCheckpointExtStore(deps.Repo.ORM())
	}
	v2.RegisterActivity(registry, handlers.NewLoadWorkflowActivity(deps.Repo, workspaceRows))
	v2.RegisterActivity(registry, handlers.NewPreflightDaemonCheckActivity(deps.Repo, launchEvents, workspaceRows, deps.ToolExecutor))
	v2.RegisterActivity(registry, handlers.NewPreflightRequirementsActivity(deps.Repo, workspaceRows, deps.ToolExecutor, deps.Connections).WithDaemonRouter(deps.DaemonRouter))
	v2.RegisterActivity(registry, handlers.NewMachineResumeWakeActivity(deps.Repo, launchEvents, workspaceRows, deps.ToolExecutor))
	v2.RegisterActivity(registry, handlers.NewMachinePreflightActivity(deps.Repo, launchEvents, workspaceRows, deps.DaemonRouter))
	v2.RegisterActivity(registry, handlers.NewGreenfieldProbeActivity(deps.Repo, deps.DaemonRouter))
	v2.RegisterLifecycleActivity(registry, handlers.NewWorkflowStatusActivity(handlers.WorkflowStatusDeps{Repo: deps.Repo, Triggers: statusTriggers, Updates: chatRefetcher(updates), Runs: runRecords, Workflows: workflowRuns, Attention: attention, RunEvents: runEvents, Pauser: pauser}))
	v2.RegisterLifecycleActivity(registry, handlers.NewThreadStatusActivity(handlers.ThreadStatusDeps{Store: deps.Repo, Mailbox: mailboxStore.resolver}))
	v2.RegisterLifecycleActivity(registry, handlers.NewUsageLimitWaitActivity(handlers.UsageLimitWaitDeps{Store: deps.Repo, Waits: deps.ProviderBackoff, Threads: deps.Threads}))
	v2.RegisterLifecycleActivity(registry, handlers.NewTriggerCapacitySlotActivity(handlers.TriggerCapacitySlotDeps{Slots: capacitySlots}))
	v2.RegisterLifecycleActivity(registry, handlers.NewWorkflowCheckpointActivity(handlers.WorkflowCheckpointDeps{Checkpoints: checkpoints}))
	v2.RegisterLifecycleActivity(registry, handlers.NewWorkflowErrorActivity(deps.Repo))
	var toolBlocks handlers.ToolResultFinder
	if deps.Repo != nil {
		toolBlocks = store.NewMessageContentBlockExtStore(deps.Repo.ORM())
	}
	v2.RegisterLifecycleActivity(registry, handlers.NewCleanupActivity(handlers.CleanupDeps{Store: deps.Repo, ToolBlocks: toolBlocks, Approvals: approvals, Workflows: workflowRuns}))
	v2.RegisterLifecycleActivity(registry, handlers.NewEmitStreamFinalizedActivity(deps.Repo))
	v2.RegisterActivity(registry, handlers.NewCreateWorkflowWithThreadActivity(deps.Threads, deps.Repo, workflowRuns))

	// ========================================================================
	// UTILITY ACTIVITIES
	// ========================================================================

	v2.RegisterActivity(registry, handlers.NewUnknownStepTypeActivity())
	v2.RegisterActivity(registry, handlers.NewFailStepActivity())
	v2.RegisterActivity(registry, handlers.NewRecordToleratedFailureActivity())
	v2.RegisterActivity(registry, handlers.NewSkippedStepActivity())
	v2.RegisterActivity(registry, handlers.NewFetchThreadResultActivity(deps.Threads))
	v2.RegisterActivity(registry, handlers.NewValidateThreadOwnershipActivity(deps.Repo))
	v2.RegisterActivity(registry, handlers.NewLoadPresetParamsActivity(deps.Repo))
	v2.RegisterObservabilityActivity(registry, handlers.NewEmitToolCallStatusActivity(deps.Repo))
	v2.RegisterActivity(registry, handlers.NewDrainAgentMessagesActivity(handlers.DrainAgentMessagesDeps{Store: deps.Repo, Mailbox: mailboxStore.queue, Threads: deps.Threads}))
	v2.RegisterActivity(registry, handlers.NewEnqueueAgentMessageActivity(handlers.EnqueueAgentMessageDeps{Mailbox: mailboxStore.enqueue}))
	v2.RegisterActivity(registry, handlers.NewLookupStoppedSubAgentActivity(stoppedAgents))

}

// initPreflightConfig sets up the PreflightConfig for RequiresDaemon.
// This bridges the tools and runtime packages which can't import each other.
func initPreflightConfig() {
	v2.SetPreflightConfig(newPreflightConfig())
}

// newPreflightConfig builds the production PreflightConfig.
func newPreflightConfig() *v2.PreflightConfig {
	return tools.PreflightConfig()
}

// agentMessageStores is the mailbox's roles over one database: the generated
// store for the queue, the resolver and the preemption write, and the Mailbox
// for enqueues that must be announced. All nil when there is no database, as
// every other optional role in RegisterAll.
type agentMessageStores struct {
	queue    handlers.MailboxQueue
	resolver handlers.MailboxResolver
	preempt  handlers.MailboxPreempter
	enqueue  handlers.EnqueueAgentMessageStore
}

// userRefetcher and chatRefetcher turn an absent writer into a nil role: a nil
// chatupdates.Service converted to an activity's role is not nil.
func userRefetcher(u chatupdates.Service) handlers.UserRefetcher {
	if u == nil {
		return nil
	}
	return u
}

func chatRefetcher(u chatupdates.Service) handlers.ChatRefetcher {
	if u == nil {
		return nil
	}
	return u
}
