// Copyright (c) 2025 Reliant Labs
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
//
//forge:exclude-contract: workflow pause/resume/reset orchestration over the Temporal client; Temporal-SDK shaped
package workflow

import (
	"time"

	"github.com/google/uuid"
)

// Workflow names
const (
	// WorkflowThread removed - no longer used
	WorkflowDynamic = "DynamicWorkflow"
)

// Worker configuration
const (
	// SharedTaskQueue is the single task queue used by all workflow workers.
	// All workflows share this queue - pause/resume is handled via signals, not worker lifecycle.
	SharedTaskQueue = "reliant-workflows"

	// TemporalNamespace is the Temporal namespace used by all workflows.
	TemporalNamespace = "reliant"

	// WorkflowTaskQueue is the shared queue used by DynamicWorkflow executions.
	WorkflowTaskQueue = "chat-workflows"
)

// Default workflow constants
const (
	// DefaultWorkflow is the default workflow used when no workflow is specified.
	// Users can override this via their preferences.
	DefaultWorkflow = "builtin://agent"
)

// WorkflowExecutionTimeout is how long a paused workflow stays alive in Temporal
// before expiring: 14 days.
const WorkflowExecutionTimeout = 14 * 24 * time.Hour

// DynamicWorkflowTaskTimeout bounds one workflow task — a replay of the run's
// history plus the code between two commands — for every DynamicWorkflow.
//
// Temporal's 10s default is sized for small histories. A long chat's root
// run reaches tens of thousands of events, and replaying that on a loaded
// worker took up to 42.5s per task; at 10s each such task timed out and was
// retried from scratch, stalling the whole run for minutes. That stall is
// what let a reconciler pass mistake a finishing background spawn for a lost
// one (dev-docs/incidents/2026-10-04-spawn-report-collision.md).
//
// 60s covers the worst task observed with margin and stays under Temporal's
// 120s ceiling. Continue-as-new and reset inherit it from the run they
// replace, so setting it at start covers the whole chain. It does not fix
// history growth — continue-as-new is what bounds that.
const DynamicWorkflowTaskTimeout = 60 * time.Second

// Signal names
const (
	SignalPause  = "signal.pause"  // Signal to pause workflow at next step boundary
	SignalResume = "signal.resume" // Signal to resume a paused workflow
	// SignalUpdateWorkflowState carries an input update (model, mode,
	// tools...) the run applies to its live inputs.
	SignalUpdateWorkflowState = "update_workflow_state"
)

// RuntimeInjectedInputs is a set of input names that are injected at runtime
// and should not be validated against the workflow's input schema.
// These are internal values used by the workflow engine, not user-provided inputs.
var RuntimeInjectedInputs = map[string]bool{
	"_root_first_run_id": true,
	"project_path":       true, // Used for preset loading in spawned workflows; workflow.path
	"worktree_path":      true, // The chat's worktree, when it has one; workflow.worktree_path
	"worktree_branch":    true, // That worktree's branch; workflow.branch
	"chat_id":            true, // Injected after validation
	"workflow_id":        true, // Injected after validation
	"unique_activity_id": true, // Injected after validation
	"spawned_by":         true, // Injected after validation
	"__thread":           true, // Internal signal routing key for thread-scoped updates
	"session_daemon_id":  true, // Session-level active daemon for tool execution
	"__trigger":          true, // Launch event, exposed to CEL as `trigger`
	// No human is watching (runtime.InputKeyUnattended). Injected by the
	// launcher for trigger runs; the runtime's own input validation runs
	// inside the workflow and rejected it as unknown, so every trigger fire of
	// a workflow with declared inputs failed before its first node.
	"unattended": true,
	// The run the chat's launch started, as opposed to one a person's reply
	// started later (runtime.InputKeyLaunchRun). Injected by the launcher only.
	"__launch_run": true,
	// The provider(s) the user chose for the run, carried to every child
	// (runtime.InputKeyProviderPreference). Injected by the runtime only.
	"__provider_preference": true,
	"__chat_provider":       true,
	"__preset_model":        true,
	// What the run does when its provider is rate-limited
	// (runtime.InputKeyProviderPolicy). Injected by the launcher, a preset or a
	// spawn call's `providers:`, never taken from a client.
	"__provider_policy": true,
	// Who the run's integration writes speak as, and which automation it is
	// (runtime.InputKeyActingIdentity / InputKeyAutomation). Injected by the
	// launcher only; a reply's run never carries them.
	"__acting_identity": true,
	"__automation":      true,
	// What failed, for the nodes of an on_failure handler (runtime.Failure).
	// Set by the runtime for the handler only, never taken from a client.
	"__failure": true,
}

// NewWorkflowID generates a new random UUID for a root workflow.
// Workflow IDs are always UUIDs - additional context (workflow_name, chat_id, parent_workflow_id)
// should be stored as separate fields in the database, not concatenated into the ID.
func NewWorkflowID() string {
	return uuid.New().String()
}
