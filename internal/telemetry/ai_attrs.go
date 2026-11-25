// Copyright (c) 2025 Reliant Labs
package telemetry

import "github.com/reliant-labs/forge/pkg/observe/genai"

// The names of the spans and attributes that make a workflow run readable as
// an AI run: workflow -> node -> agent turn -> LLM call / tool call. Every name
// a reliant span emits for AI monitoring is declared here and nowhere else.
//
// gen_ai.* are the OpenTelemetry GenAI semantic conventions. forge.ai.* name
// what the conventions do not cover yet (the workflow, its nodes and the run).
// Each constant forge's pkg/observe/genai declares is an alias of forge's, so
// reliant's spans and forge's built-in AI dashboards (pkg/obsbuiltin) cannot
// drift apart; the few forge does not declare (the tool type and location
// values, the node span's name) are spelled as forge's starters emit them.
// They are a public contract once released: never rename one in place, add the
// new name beside it.
const (
	// AttrGenAIOperationName says what kind of AI span a span is; its values
	// are the GenAIOp* constants.
	AttrGenAIOperationName = genai.AttrOperationName
	// AttrGenAIConversationID is the conversation: the chat the work
	// belongs to.
	AttrGenAIConversationID = genai.AttrConversationID
	// AttrGenAIAgentID is the agent: the thread whose turns it takes.
	AttrGenAIAgentID = genai.AttrAgentID
	// AttrGenAIAgentName is the agent's preset, or its workflow when it runs
	// without one.
	AttrGenAIAgentName = genai.AttrAgentName
	// AttrGenAIToolName, AttrGenAIToolCallID and AttrGenAIToolType describe
	// one tool call.
	AttrGenAIToolName   = genai.AttrToolName
	AttrGenAIToolCallID = genai.AttrToolCallID
	AttrGenAIToolType   = genai.AttrToolType

	// AttrAIWorkflowName is the workflow definition a run executes (agent,
	// a project workflow), which the Temporal span names only as
	// DynamicWorkflow.
	AttrAIWorkflowName = genai.AttrWorkflowName
	// AttrAIWorkflowRef is the reference the run was started with
	// ("builtin://agent", a project workflow's path); AttrAIWorkflowName is
	// its name. AttrAIWorkflowTrigger is what started the run: a trigger's
	// kind, "manual" for a person, "spawn" for a sub-agent.
	AttrAIWorkflowRef     = genai.AttrWorkflowRef
	AttrAIWorkflowTrigger = genai.AttrWorkflowTrigger
	// AttrAIRunID is the run, stable across continue-as-new and a resume of
	// an interrupted run: the run explorer groups a run's spans by it, not by
	// trace id. A spawned sub-agent is a run of its own.
	AttrAIRunID = genai.AttrRunID
	// AttrAIRootRunID is the top of a spawn chain: on a top-level run's
	// spans it equals AttrAIRunID. AttrAIParentRunID is the run that spawned
	// this one, set on a spawned run's spans only.
	AttrAIParentRunID = genai.AttrParentRunID
	AttrAIRootRunID   = genai.AttrRootRunID
	// AttrAINodeID, AttrAINodeType and AttrAINodeIteration are the workflow
	// node the work ran in: its id in its workflow, its type (call_llm,
	// execute_tools, run, ...) and its iteration: the enclosing loop's, 0
	// outside a loop.
	AttrAINodeID        = genai.AttrNodeID
	AttrAINodeType      = genai.AttrNodeType
	AttrAINodeIteration = genai.AttrNodeIteration
	// AttrAIToolLocation is where a tool ran: ToolLocationDaemon (the user's
	// machine, through the gateway) or ToolLocationLocal (the worker).
	AttrAIToolLocation = genai.AttrToolLocation
	// AttrAICostUSD is a call's cost in US dollars as the provider or gateway
	// reported it; never an estimate. Emitted beside AttrReliantCostUSD,
	// the name reliant reported it under first, which forge reads but does
	// not emit.
	AttrAICostUSD      = genai.AttrCostUSD
	AttrReliantCostUSD = genai.AttrCostUSDReliant

	// AttrOrgID is the org the work is for, beside the user (AttrUserID).
	AttrOrgID = genai.AttrOrgID
	// AttrErrorType is the class of a failure; ErrorTypeCanceled marks a
	// cancellation, which is not one.
	AttrErrorType     = genai.AttrErrorType
	ErrorTypeCanceled = genai.ErrorTypeCanceled

	// AttrGenAIRetries is how many times a model call was retried before it
	// returned.
	AttrGenAIRetries = genai.AttrRetries
)

// Span events of an LLM call.
const (
	// EventRetryWait is recorded once per backoff before a retry.
	EventRetryWait = genai.EventRetryWait
	// EventFirstToken is recorded when a streamed response's first token
	// arrives: the span's start to it is the time to first token.
	EventFirstToken = genai.EventFirstToken
)

// Values of AttrGenAIOperationName.
const (
	GenAIOpChat        = genai.OpChat
	GenAIOpInvokeAgent = genai.OpInvokeAgent
	GenAIOpExecuteTool = genai.OpExecuteTool
	// GenAIOpInvokeWorkflow marks a workflow run's span. The conventions have
	// no workflow operation yet; this is forge's.
	GenAIOpInvokeWorkflow = genai.OpInvokeWorkflow
)

// Values of AttrGenAIToolType and AttrAIToolLocation.
const (
	GenAIToolTypeFunction = "function"
	ToolLocationDaemon    = "daemon"
	ToolLocationLocal     = "local"
)

// SpanNameNode is the prefix of a workflow node's span: "workflow.node <id>",
// the name genai.StartNode gives it.
const SpanNameNode = "workflow.node"
