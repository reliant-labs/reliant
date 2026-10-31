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

	// Failover is what the run allows when a provider's accounts are all
	// limited (see FailoverPolicy). Set on call_llm nodes only.
	Failover FailoverPolicy `json:"failover,omitzero"`

	// FallbackModel replaces the chat's model selector: the run is continuing
	// on another provider until the original one is back. The workflow sets
	// it from its lease and clears it at the lease's end, which is
	// deterministic because the comparison is made in the workflow. Set on
	// call_llm nodes only.
	FallbackModel string `json:"fallback_model,omitempty"`

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

	// CatalogPin is the workflow catalog the run started with
	// (db.Repo.PinProjectCatalog). call_llm reads the presets its spawn tool
	// describes from it, so the model is offered the presets a spawn then
	// loads. Set on call_llm nodes; empty for a run with no pin.
	CatalogPin string `json:"catalog_pin,omitempty"`

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

// SpawnConfig holds the configuration for spawning child workflows.
type SpawnConfig struct {
	Workflow string   `json:"workflow"`
	Presets  []string `json:"presets,omitempty"`
}
