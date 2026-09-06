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
	// The dotted convention matches what scenarios already use for `reached:` /
	// `not_reached:` and what the fast simulator emits (simulator.go builds
	// "loop_id.inner_node" the same way), which is what lets the Temporal-backed
	// harness report the same node names the simulator does.
	NodePath string `json:"node_path,omitempty"`

	// Context sequence for compaction
	ContextSequence *int `json:"context_sequence,omitempty"`

	// Spawn context
	SpawnedBy        string       `json:"spawned_by,omitempty"`
	SpawnDepth       int          `json:"spawn_depth,omitempty"`
	SpawnConfig      *SpawnConfig `json:"spawn_config,omitempty"`
	ParentPermission string       `json:"parent_permission,omitempty"` // Cap child permission to parent's level

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
}

// SpawnConfig holds the configuration for spawning child workflows.
type SpawnConfig struct {
	Workflow string   `json:"workflow"`
	Presets  []string `json:"presets,omitempty"`
}
