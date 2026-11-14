// Copyright (c) 2025 Reliant Labs
package telemetry

// The names of the spans and attributes that make a workflow run readable as
// an AI run: workflow -> node -> agent turn -> LLM call / tool call. Every name
// a reliant span emits for AI monitoring is declared here and nowhere else.
//
// gen_ai.* are the OpenTelemetry GenAI semantic conventions. forge.ai.* name
// what the conventions do not cover yet (the workflow, its nodes and the run),
// spelled exactly as forge's pkg/observe/genai declares them and its built-in
// AI dashboards (pkg/obsbuiltin) read them (forge #2226). When reliant pins a
// forge release that has that package, each constant below becomes an alias
// of forge's and nothing else changes. They are a public contract once released: never
// rename one in place, add the new name beside it.
const (
	// AttrGenAIOperationName says what kind of AI span a span is; its values
	// are the GenAIOp* constants.
	AttrGenAIOperationName = "gen_ai.operation.name"
	// AttrGenAIConversationID is the conversation: the chat the work
	// belongs to.
	AttrGenAIConversationID = "gen_ai.conversation.id"
	// AttrGenAIAgentID is the agent: the thread whose turns it takes.
	AttrGenAIAgentID = "gen_ai.agent.id"
	// AttrGenAIAgentName is the agent's preset, or its workflow when it runs
	// without one.
	AttrGenAIAgentName = "gen_ai.agent.name"
	// AttrGenAIToolName, AttrGenAIToolCallID and AttrGenAIToolType describe
	// one tool call.
	AttrGenAIToolName   = "gen_ai.tool.name"
	AttrGenAIToolCallID = "gen_ai.tool.call.id"
	AttrGenAIToolType   = "gen_ai.tool.type"

	// AttrAIWorkflowName is the workflow definition a run executes (agent,
	// a project workflow), which the Temporal span names only as
	// DynamicWorkflow.
	AttrAIWorkflowName = "forge.ai.workflow.name"
	// AttrAIWorkflowRef is the reference the run was started with
	// ("builtin://agent", a project workflow's path); AttrAIWorkflowName is
	// its name. AttrAIWorkflowTrigger is what started the run: a trigger's
	// kind, "manual" for a person, "spawn" for a sub-agent.
	AttrAIWorkflowRef     = "forge.ai.workflow.ref"
	AttrAIWorkflowTrigger = "forge.ai.workflow.trigger"
	// AttrAIRunID is the run, stable across continue-as-new and a resume of
	// an interrupted run: the run explorer groups a run's spans by it, not by
	// trace id. A spawned sub-agent is a run of its own.
	AttrAIRunID = "forge.ai.run.id"
	// AttrAIRootRunID is the top of a spawn chain: on a top-level run's
	// spans it equals AttrAIRunID. AttrAIParentRunID is the run that spawned
	// this one, set on a spawned run's spans only.
	AttrAIParentRunID = "forge.ai.parent_run_id"
	AttrAIRootRunID   = "forge.ai.root_run_id"
	// AttrAINodeID, AttrAINodeType and AttrAINodeIteration are the workflow
	// node the work ran in: its id in its workflow, its type (call_llm,
	// execute_tools, run, ...) and its iteration: the enclosing loop's, 0
	// outside a loop.
	AttrAINodeID        = "forge.ai.node.id"
	AttrAINodeType      = "forge.ai.node.type"
	AttrAINodeIteration = "forge.ai.node.iteration"
	// AttrAIToolLocation is where a tool ran: ToolLocationDaemon (the user's
	// machine, through the gateway) or ToolLocationLocal (the worker).
	AttrAIToolLocation = "forge.ai.tool.location"
	// AttrAICostUSD is a call's cost in US dollars as the provider or gateway
	// reported it; never an estimate. Emitted beside AttrReliantCostUSD,
	// the name reliant reported it under first.
	AttrAICostUSD      = "forge.ai.cost.usd"
	AttrReliantCostUSD = "reliant.cost_usd"

	// AttrOrgID is the org the work is for, beside the user (AttrUserID).
	AttrOrgID = "org.id"
	// AttrErrorType is the class of a failure; ErrorTypeCanceled marks a
	// cancellation, which is not one.
	AttrErrorType     = "error.type"
	ErrorTypeCanceled = "canceled"
)

// Span events of an LLM call.
const (
	// EventRetryWait is recorded once per backoff before a retry.
	EventRetryWait = "gen_ai.retry_wait"
	// EventFirstToken is recorded when a streamed response's first token
	// arrives: the span's start to it is the time to first token.
	EventFirstToken = "gen_ai.first_token" //nolint:gosec // G101: an OpenTelemetry span event name, not a credential
)

// Values of AttrGenAIOperationName.
const (
	GenAIOpChat        = "chat"
	GenAIOpInvokeAgent = "invoke_agent"
	GenAIOpExecuteTool = "execute_tool"
	// GenAIOpInvokeWorkflow marks a workflow run's span. The conventions have
	// no workflow operation yet; this is forge's.
	GenAIOpInvokeWorkflow = "invoke_workflow"
)

// Values of AttrGenAIToolType and AttrAIToolLocation.
const (
	GenAIToolTypeFunction = "function"
	ToolLocationDaemon    = "daemon"
	ToolLocationLocal     = "local"
)

// SpanNameNode is the prefix of a workflow node's span: "workflow.node <id>".
const SpanNameNode = "workflow.node"
