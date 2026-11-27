// Copyright (c) 2025 Reliant Labs
package types

// RuntimeContext contains fields injected by the workflow engine at execution time.
// This is a pure Go struct serialized as JSON over Temporal.
//
// Placed in activities/types to avoid import cycles between runtime engine <-> handlers.
type RuntimeContext struct {
	// Core identifiers
	ChatID     string `json:"chat_id"`
	Thread     string `json:"thread"`
	WorkflowID string `json:"workflow_id,omitempty"`
	StepID     string `json:"step_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	// Excludes automatic saved memory only; never grants or revokes tools.
	ExcludeAmbientMemory bool `json:"exclude_ambient_memory,omitempty"`

	// AssistantMessageID is the pre-allocated id for the assistant message a
	// call_llm activity will stream (delta identity protocol). Minted by the
	// workflow via SideEffect so retries re-stream under the same id. Empty
	// for legacy histories and non-LLM activities.
	AssistantMessageID string `json:"assistant_message_id,omitempty"`

	// MessageIdempotencyKey, when non-empty, IS the idempotency key SaveMessage
	// persists and dedupes on, replacing the per-run key the activity would
	// otherwise derive from its Temporal RunID.
	//
	// Set it only for a message whose identity is the position in the workflow
	// graph rather than the activity execution that happened to write it — the
	// inject message seeded into a child thread is the one such message today
	// (see runtime.injectIdempotencyKey). A resumed run gets a NEW RunID, so a
	// RunID-scoped key cannot recognize the seed it already wrote and the child
	// agent is told a second time to start work it had already started.
	//
	// Callers that do NOT set this keep RunID scoping, which is what they want:
	// an assistant or tool message belongs to the run that produced it.
	MessageIdempotencyKey string `json:"message_idempotency_key,omitempty"`

	// SaveMessage, when set, delegates this node's save_message to the
	// ActivityWrapper: the worker writes the message right after the activity
	// returns. Nil means the workflow does not want the activity to save
	// anything (no save_message, or the workflow saves it itself).
	SaveMessage *SaveMessageRequest `json:"save_message,omitempty"`

	// Observe, when set, hands this node's observe: block to the
	// ActivityWrapper, which evaluates and records it after the activity
	// returns (and after the save_message). Nil means the node is not
	// observed here.
	Observe *ObserveRequest `json:"observe,omitempty"`

	// Loop context
	LoopNodeID    string `json:"loop_node_id,omitempty"`
	LoopIteration int    `json:"loop_iteration,omitempty"`

	// NodePath is the fully-qualified dotted position of this node in the graph,
	// composed through every nesting boundary: "impl_loop.attempt.review" for a
	// node inside a sub-workflow inside a loop. A top-level node's path is just
	// its own id.
	//
	// This is NOT LoopNodeID with dots. LoopNodeID means LOOP-SCOPED IDENTITY —
	// which loop iteration a row belongs to — and it is a real column on
	// step_executions, questions and yields with a unique index over
	// (workflow_id, loop_node_id, loop_iteration). Composing a path into it
	// would change what those rows mean. NodePath is observability and test
	// assertion only, and is deliberately not persisted anywhere.
	//
	// The dotted convention matches what scenarios use for `reached:` /
	// `not_reached:`, which is what lets the scenario runner report node names
	// a scenario can assert on.
	NodePath string `json:"node_path,omitempty"`

	// Context sequence for compaction
	ContextSequence *int `json:"context_sequence,omitempty"`

	// Spawn context
	SpawnedBy        string       `json:"spawned_by,omitempty"`
	SpawnDepth       int          `json:"spawn_depth,omitempty"`
	SpawnConfig      *SpawnConfig `json:"spawn_config,omitempty"`
	ParentPermission string       `json:"parent_permission,omitempty"` // Cap child permission to parent's level

	// ToolGrants are the tools load_tool has granted on this thread so far,
	// recorded by the workflow from each ExecuteTools result's granted_tools
	// and handed to every call_llm on the thread. Workflow state, not process
	// state: it survives a worker restart, another replica, and
	// continue-as-new. See research/TOOL_CAPABILITIES.md.
	ToolGrants []string `json:"tool_grants,omitempty"`

	// Unattended is the run's runtime.IsUnattended: nobody is attending it. A
	// trigger-fired run and every sub-workflow and spawned sub-agent of one
	// carry it; a person's turn never does. call_llm resolves the turn's
	// capability set with it, so it is set on call_llm nodes only.
	Unattended bool `json:"unattended,omitempty"`
	// Approved preview hashes and refusals are runtime-authored, never model inputs.
	ActivationApprovals map[string]string `json:"activation_approvals,omitempty"`
	ActivationRefusals  map[string]string `json:"activation_refusals,omitempty"`
	// ApprovedConnections maps a propose_connection call approved on its card
	// to the connection id the person made in the connection dialog. Only an
	// id: the credential never enters the run.
	ApprovedConnections map[string]string `json:"approved_connections,omitempty"`

	// ProviderPreference is the run's runtime.ProviderPreference: the
	// provider(s) the user chose for the run. call_llm hands it to a model
	// selector that names no provider of its own, so a sub-agent's tier
	// resolves on the provider the user picked instead of whichever connected
	// provider leads the tier. Set on call_llm nodes only.
	ProviderPreference []string `json:"provider_preference,omitempty"`
	// ChatProvider strictly binds the next model call to the provider the
	// user selected chat-wide. Descendants keep their original semantic tags;
	// PresetModelTags is provenance captured before an exact override.
	ChatProvider      string   `json:"chat_provider,omitempty"`
	ClearProviderPins bool     `json:"clear_provider_pins,omitempty"`
	Descendant        bool     `json:"descendant,omitempty"`
	PresetModelTags   []string `json:"preset_model_tags,omitempty"`
	// ChatModel is the chat's own model selection (the root thread's model
	// input) on a bound chat. A descendant whose own selector cannot run on
	// ChatProvider runs on it instead of holding, unless the user chose the
	// descendant's model themselves (ThreadModelChosen): a preset's, a spawn
	// argument's or a workflow's model is a default, not a user choice, and a
	// default that the chat's provider cannot serve must not park the thread.
	ChatModel         *ModelChoice `json:"chat_model,omitempty"`
	ThreadModelChosen bool         `json:"thread_model_chosen,omitempty"`

	// Failover is what the run allows when a provider's accounts are all
	// limited (see FailoverPolicy). Set on call_llm nodes only.
	Failover FailoverPolicy `json:"failover,omitzero"`

	// FallbackModel replaces the chat's model selector: the run is continuing
	// on another provider until the original one is back. The workflow sets
	// it from its lease and clears it at the lease's end, which is
	// deterministic because the comparison is made in the workflow. Set on
	// call_llm and compact nodes.
	FallbackModel string `json:"fallback_model,omitempty"`
	// ManualFallback marks an active banner lease. Compaction uses its exact
	// model directly instead of first resolving the old summarization selector.
	ManualFallback bool `json:"manual_fallback,omitempty"`

	// FallbackReturn is set on the first call_llm after a fallback lease
	// ended, so the transcript can say the chat is back on its own model.
	FallbackReturn *FallbackReturn `json:"fallback_return,omitempty"`

	// ActingIdentity is who this run's integration writes speak as, read off
	// the run's inputs (runtime.ActingIdentity): "app" for the GitHub App's bot,
	// empty for the owner. Set on every node, because an `action` node and an
	// agent's integration tool both resolve their credential from it. Replay
	// safe: it is a pure function of inputs the workflow already holds.
	ActingIdentity string `json:"acting_identity,omitempty"`
	// Automation names the trigger the run belongs to, for the footer on what
	// the bot writes.
	Automation string `json:"automation,omitempty"`

	// SpeculativeTurn marks a call_llm turn the agent loop took on a hint
	// rather than on known input: its loop-exit gate re-entered because a
	// wake (a mailbox row or a user message) or a background spawn's report
	// arrived after the previous turn began, and that turn may already have
	// read it. If it did, this turn finds history ending with the assistant
	// and yields without calling the provider — the expected answer to a
	// hint that was already consumed, where on any other turn the same yield
	// means something upstream is wrong. Set on call_llm nodes only.
	SpeculativeTurn bool `json:"speculative_turn,omitempty"`

	// ResumeTurn marks the first call_llm turn of an agent loop resumed after
	// it stopped mid-work: a sub-agent that stopped on a failure and is
	// resumed (by a capacity wake, a message, or a successor run), or a root
	// loop re-entered at its checkpointed position. Such a loop never
	// finished its turn, so an assistant message at the tail of its history
	// is the turn that was cut off, not an answer. When the resume brought no
	// new input (the mailbox drain leaves that tail in place), call_llm adds
	// a hidden "continue" user message and calls the provider, instead of
	// yielding the cut-off turn as the agent's last word. Set on call_llm
	// nodes only, for one turn.
	ResumeTurn bool `json:"resume_turn,omitempty"`

	// CutOffMessageID is set on a call_llm step re-dispatched after a pause
	// cancelled its previous dispatch mid-stream: the pre-allocated id that
	// dispatch streamed under, and so the id its partial assistant message
	// was persisted under (persistInterruptedTurn). When history ends with
	// that message and nothing new arrived, the partial is this turn's own
	// cut-off output, not an answer: call_llm continues it, as on a
	// ResumeTurn, instead of yielding it as the agent's last word. Matched
	// by id, so an assistant tail that is a finished answer still yields.
	// Set on call_llm nodes only, for one dispatch (reliant #2759).
	CutOffMessageID string `json:"cut_off_message_id,omitempty"`

	// InterruptedTurn is set on an execute_tools step dispatched to settle a
	// batch a same-thread interrupt cancelled ("Interrupt & send now"). The
	// activity runs nothing: a call a previous dispatch already finished
	// keeps its recorded result, and every other call is recorded Cancelled
	// and answered as stopped by the user. So every tool call of the turn
	// gets a tool_result, its save_message keeps the history valid, and the
	// loop reaches the call_llm that delivers the thread's mailbox, instead
	// of running the batch (and its approval previews) again. Set on
	// execute_tools nodes only, for one dispatch.
	InterruptedTurn bool `json:"interrupted_turn,omitempty"`

	// CatalogPin is the workflow catalog the run started with
	// (db.Repo.PinProjectCatalog). call_llm reads the presets its spawn tool
	// describes from it, so the model is offered the presets a spawn then
	// loads. Set on call_llm nodes; empty for a run with no pin.
	CatalogPin string `json:"catalog_pin,omitempty"`

	// ServerActionFrom is the node a Reliant server action's `with: {from:
	// <id>}` names, as the engine holds it: its declared type and role from
	// the definition the run executes, and the output it recorded. A
	// verdict-writing action reads the reviewer's verdict here, never from
	// its own `with:`, which the workflow's author renders. Set on server
	// action nodes only, and only when the named node is in the action's
	// own scope.
	ServerActionFrom *ServerActionFrom `json:"server_action_from,omitempty"`

	// Project context
	ProjectPath string `json:"project_path,omitempty"`

	// Daemon targeting - specifies which daemon should execute tools.
	// Set from workflow-level or node-level daemon field. nil means use default resolution.
	DaemonSelector *DaemonSelector `json:"daemon_selector,omitempty"`

	// UserJWT carries the user's bearer token into the activity so workers in a
	// separate process (where the gRPC auth interceptor never ran) can hydrate the
	// in-memory JWT map and resolve the Reliant LLM driver. Token persists into
	// workflow history; rely on its natural expiry as the security boundary.
	UserJWT string `json:"user_jwt,omitempty"`

	// Telemetry names the run this activity works for, for AI monitoring: the
	// worker's spans carry it (internal/telemetry/ai_attrs.go). Set by the
	// runtime, never authored; nil from a workflow that predates it.
	Telemetry *RunTelemetry `json:"telemetry,omitempty"`
}

// RunTelemetry is a run's identity on its spans (internal/telemetry): which
// run, which workflow, which agent. It rides the execution context, so every
// node of the run carries the same one; a spawned sub-agent is a run of its
// own whose ids name the run that spawned it.
type RunTelemetry struct {
	// WorkflowName is the workflow the run executes; WorkflowRef is the
	// reference it was started with ("builtin://agent", a project path).
	WorkflowName string `json:"workflow_name,omitempty"`
	WorkflowRef  string `json:"workflow_ref,omitempty"`
	// Trigger is what started the run: a trigger's kind, "manual" for a
	// person, "spawn" for a sub-agent.
	Trigger string `json:"trigger,omitempty"`
	// RunID is forge.ai.run.id: the root run's id survives continue-as-new
	// and a resume of an interrupted run; a sub-agent's is its workflow id.
	RunID string `json:"run_id,omitempty"`
	// ParentRunID and RootRunID are set on a spawned sub-agent's run only.
	ParentRunID string `json:"parent_run_id,omitempty"`
	RootRunID   string `json:"root_run_id,omitempty"`
	// Preset is the agent's preset, when the runtime resolved one.
	Preset string `json:"preset,omitempty"`
	// TurnTraceParent, on an execute_tools node only, is the traceparent of
	// the call_llm turn whose tool calls it runs (CallLLMOutput.trace_parent).
	TurnTraceParent string `json:"turn_traceparent,omitempty"`
}

// ModelChoice is a model selector as a workflow input holds it: an exact id
// ("<model>" or "<model>@<driver>"), semantic tags, and preferred providers.
type ModelChoice struct {
	ID        string   `json:"id,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Providers []string `json:"providers,omitempty"`
}

// DaemonSelector specifies criteria for selecting which daemon executes tools.
type DaemonSelector struct {
	ID     string            `json:"id,omitempty"`
	Name   string            `json:"name,omitempty"`
	Type   string            `json:"type,omitempty"` // "local", "cloud", "any"
	Labels map[string]string `json:"labels,omitempty"`
	// Session marks the chat's pin (session_daemon_id, or the chat's
	// active_daemon_id read at activity time) rather than a selector the
	// workflow or node configures. Only a session pin yields to a branch
	// workspace's owner (toolDaemonSelector).
	Session bool `json:"session,omitempty"`
}

// ServerActionFrom is a node of the run's graph as the engine recorded it,
// for the server action that names it (RuntimeContext.ServerActionFrom).
type ServerActionFrom struct {
	// NodeID is the node's id in the action's scope.
	NodeID string `json:"node_id"`
	// Type is the node's type as the run executes it: a review node is a
	// workflow node by then (model.LowerReviewNode).
	Type string `json:"type,omitempty"`
	// Role is the engine role the definition gives it (model.RoleReviewer).
	Role string `json:"role,omitempty"`
	// Review is true only for an engine-owned review node
	// (model.IsLoweredReviewNode): its output is the checked verdict and the
	// reviewer session it ran in.
	Review bool `json:"review,omitempty"`
	// Ran is true when the node has an output in this scope.
	Ran bool `json:"ran,omitempty"`
	// Output is the node's recorded output; nil when it has not run.
	Output map[string]interface{} `json:"output,omitempty"`
}

// SpawnConfig holds the configuration for spawning child workflows.
type SpawnConfig struct {
	Workflow string   `json:"workflow"`
	Presets  []string `json:"presets,omitempty"`
	// SpawnerDepth is the spawn depth of the agent offered this spawn tool:
	// 0 for a root agent, 1 for a sub-agent. The tool's description tells
	// the agent whether the sub-agents it starts may spawn their own.
	SpawnerDepth int `json:"spawner_depth,omitempty"`
}
