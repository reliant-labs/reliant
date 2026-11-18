package genai

// The attribute contract of an AI span: what forge's helpers stamp and what
// the built-in AI dashboards (pkg/obsbuiltin) read from otel_traces. Every name
// here is a public API (semver 1): it is never renamed in place. When OTel
// standardises a name forge holds under forge.ai.*, forge emits both and its
// readers read both.
//
// The gen_ai.* names are the OpenTelemetry GenAI semantic conventions
// (https://opentelemetry.io/docs/specs/semconv/gen-ai/). The forge.ai.* names
// cover what the conventions do not name yet: the workflow, its run and its
// nodes, the tool's location and a gateway-reported cost.

// Operation names (gen_ai.operation.name). Every AI span carries one, and
// the span name begins with it ("chat claude-sonnet-5", "execute_tool grep"),
// so a reader prunes on SpanName as well as the attribute.
const (
	// OpChat is a chat completion: one model call.
	OpChat = "chat"
	// OpTextCompletion is a legacy (non-chat) completion call.
	OpTextCompletion = "text_completion"
	// OpGenerateContent is a multimodal content-generation call.
	OpGenerateContent = "generate_content"
	// OpEmbeddings is an embeddings call.
	OpEmbeddings = "embeddings"
	// OpInvokeAgent is one agent turn: the model calls and tool calls an
	// agent makes to answer one input.
	OpInvokeAgent = "invoke_agent"
	// OpCreateAgent creates an agent.
	OpCreateAgent = "create_agent"
	// OpExecuteTool is one tool call.
	OpExecuteTool = "execute_tool"
	// OpInvokeWorkflow is one segment of a workflow run. The conventions
	// have no workflow operation yet; this is forge's, and a run that
	// continues (Temporal's continue-as-new, a resume) is several segments
	// sharing one AttrRunID.
	OpInvokeWorkflow = "invoke_workflow"
)

// gen_ai.* attributes (OTel GenAI semantic conventions).
const (
	// AttrOperationName is the operation (one of the Op* values). Its
	// presence is what makes a span an AI span: the sampler keeps it and the
	// dashboards select on it.
	AttrOperationName = "gen_ai.operation.name"
	// AttrProviderName is the provider ("anthropic", "openai",
	// "gcp.vertex_ai", or a gateway's own name).
	AttrProviderName = "gen_ai.provider.name"
	// AttrSystem is the conventions' older name for AttrProviderName.
	// Readers fall back to it; forge's helpers do not emit it.
	AttrSystem = "gen_ai.system"

	// AttrRequestModel is the model the caller asked for.
	AttrRequestModel = "gen_ai.request.model"
	// AttrRequestMaxTokens is the requested output-token limit.
	AttrRequestMaxTokens = "gen_ai.request.max_tokens"
	// AttrRequestTemperature is the requested sampling temperature.
	AttrRequestTemperature = "gen_ai.request.temperature"
	// AttrRequestStream says the call streamed its response. Not in the
	// conventions; Reliant emits it and forge reads it.
	AttrRequestStream = "gen_ai.request.stream"

	// AttrResponseModel is the model that served the call: a gateway's
	// fallback makes it differ from AttrRequestModel.
	AttrResponseModel = "gen_ai.response.model"
	// AttrResponseID is the provider's id for the response.
	AttrResponseID = "gen_ai.response.id"
	// AttrResponseFinishReasons are the reasons the model stopped, one per
	// choice.
	AttrResponseFinishReasons = "gen_ai.response.finish_reasons"

	// AttrUsageInputTokens is the prompt tokens the provider billed,
	// cache reads and writes included where the provider counts them so.
	AttrUsageInputTokens = "gen_ai.usage.input_tokens"
	// AttrUsageOutputTokens is the completion tokens.
	AttrUsageOutputTokens = "gen_ai.usage.output_tokens"
	// AttrUsageCacheReadInputTokens is the input tokens read from the
	// provider's prompt cache. A draft name: readers also accept
	// AttrUsageCacheReadInputTokensLegacy.
	AttrUsageCacheReadInputTokens = "gen_ai.usage.cache_read.input_tokens"
	// AttrUsageCacheReadInputTokensLegacy is an earlier draft's spelling of
	// AttrUsageCacheReadInputTokens. Read, never emitted.
	AttrUsageCacheReadInputTokensLegacy = "gen_ai.usage.cache_read_input_tokens"
	// AttrUsageCacheCreationInputTokens is the input tokens written to the
	// provider's prompt cache. A draft name.
	AttrUsageCacheCreationInputTokens = "gen_ai.usage.cache_creation.input_tokens"
	// AttrUsageCacheCreationInputTokensLegacy is an earlier draft's spelling
	// of AttrUsageCacheCreationInputTokens. Read, never emitted.
	AttrUsageCacheCreationInputTokensLegacy = "gen_ai.usage.cache_creation_input_tokens"
	// AttrUsageReasoningOutputTokens is the output tokens spent on
	// reasoning. A draft name.
	AttrUsageReasoningOutputTokens = "gen_ai.usage.reasoning.output_tokens"

	// AttrAgentID is the agent's id (in Reliant, the thread).
	AttrAgentID = "gen_ai.agent.id"
	// AttrAgentName is the agent's name (in Reliant, the preset).
	AttrAgentName = "gen_ai.agent.name"
	// AttrConversationID is the conversation the call belongs to (in
	// Reliant, the root chat).
	AttrConversationID = "gen_ai.conversation.id"

	// AttrToolName is the tool's name.
	AttrToolName = "gen_ai.tool.name"
	// AttrToolCallID is the model's id for the tool call.
	AttrToolCallID = "gen_ai.tool.call.id"
	// AttrToolType is "function", "extension" or "datastore".
	AttrToolType = "gen_ai.tool.type"

	// AttrRetries is how many times the call was retried before it
	// returned. Not in the conventions; Reliant emits it and forge reads it.
	AttrRetries = "gen_ai.retries"

	// AttrErrorType is the conventions' general error.type: the class of a
	// failure ("rate_limit", "overloaded", "timeout", an HTTP status, an
	// exception type). ErrorTypeCanceled is a cancellation, never an error.
	AttrErrorType = "error.type"
)

// Span events. Neither is in the conventions yet.
const (
	// EventRetryWait is recorded once per backoff before a retry, with the
	// wait and the status that caused it.
	EventRetryWait = "gen_ai.retry_wait"
	// EventFirstToken is recorded when a streamed response's first token
	// arrives: the span's start to it is the time to first token.
	EventFirstToken = "gen_ai.first_token"
)

// The attributes of an EventRetryWait event.
const (
	// AttrRetryAttempt is the attempt about to be retried, from 1.
	AttrRetryAttempt = "attempt"
	// AttrHTTPStatusCode is the HTTP status that caused the retry; 0 or
	// absent when the failure had none (a reset connection).
	AttrHTTPStatusCode = "http.response.status_code"
	// AttrRetryReason is the retry's cause in words ("rate_limit",
	// "overloaded", "connection_reset").
	AttrRetryReason = "reason"
	// AttrRetryDelayMs is the backoff before the retry, in milliseconds.
	AttrRetryDelayMs = "delay_ms"
)

// ErrorTypeCanceled is the AttrErrorType of a call the caller cancelled. The
// span's status stays unset: a cancellation is not a failure.
const ErrorTypeCanceled = "canceled"

// forge.ai.* attributes: what the conventions do not name yet.
const (
	// AttrWorkflowName is the workflow's name: what a "by workflow"
	// breakdown groups on. Stamp it on every span of the run (the workflow,
	// node, agent, chat and tool spans), not on the root alone.
	AttrWorkflowName = "forge.ai.workflow.name"
	// AttrWorkflowRef is the workflow's versioned reference (a file path, a
	// registry ref or a content hash).
	AttrWorkflowRef = "forge.ai.workflow.ref"
	// AttrWorkflowTrigger is what started the run ("manual", "schedule",
	// "webhook", a trigger's name).
	AttrWorkflowTrigger = "forge.ai.workflow.trigger"

	// AttrRunID is the run's id, stable across every segment of the run
	// (continue-as-new, resume, wake). A run is grouped by it, never by
	// TraceId.
	AttrRunID = "forge.ai.run.id"
	// AttrRootRunID is the top-level run a spawned run descends from; on a
	// top-level run it equals AttrRunID.
	AttrRootRunID = "forge.ai.root_run_id"
	// AttrParentRunID is the run that spawned this one; empty on a
	// top-level run.
	AttrParentRunID = "forge.ai.parent_run_id"

	// AttrNodeID is the workflow node a span ran in.
	AttrNodeID = "forge.ai.node.id"
	// AttrNodeType is the node's type ("agent", "tool", "approval", ...).
	AttrNodeType = "forge.ai.node.type"
	// AttrNodeIteration is the node's execution count within the run, from
	// 0: a node a loop revisits runs more than once.
	AttrNodeIteration = "forge.ai.node.iteration"

	// AttrToolLocation is where a tool ran ("local", "daemon", "remote").
	AttrToolLocation = "forge.ai.tool.location"

	// AttrCostUSD is the call's cost in US dollars AS REPORTED by the
	// provider or gateway (a LiteLLM response's cost). Never an estimate:
	// a call with no reported cost has no AttrCostUSD.
	AttrCostUSD = "forge.ai.cost.usd"
	// AttrCostUSDReliant is the name Reliant reported AttrCostUSD under
	// before forge named it. Read, never emitted by forge.
	AttrCostUSDReliant = "reliant.cost_usd"
)

// Who a span is for: the same keys pkg/observe stamps on product events
// (observe.AttrUserID, observe.AttrOrgID), so a user or org is one id across
// product analytics and AI spans. Opaque ids only, never an email.
const (
	// AttrUserID is the user the call was made for.
	AttrUserID = "user.id"
	// AttrOrgID is the org the call was made for.
	AttrOrgID = "org.id"
)
