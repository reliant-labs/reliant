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

	// Loop context
	LoopNodeID    string `json:"loop_node_id,omitempty"`
	LoopIteration int    `json:"loop_iteration,omitempty"`

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