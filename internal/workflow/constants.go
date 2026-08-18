// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
package workflow

import (
	"os"
	"strconv"
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

	// WorkerBuildID is a stable identifier for all Temporal workers.
	// This prevents Temporal from computing a binary checksum (which changes on every restart)
	// and ensures workers can pick up tasks from previous instances.
	// NOTE: This does NOT enable worker versioning dispatch - it just provides a stable ID.
	WorkerBuildID = "reliant-worker"

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
// before expiring. Override with RELIANT_WORKFLOW_TIMEOUT_SECONDS for testing.
var WorkflowExecutionTimeout = func() time.Duration {
	if s := os.Getenv("RELIANT_WORKFLOW_TIMEOUT_SECONDS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return 14 * 24 * time.Hour // 14 day timeout by default
}()

// Signal names
const (
	SignalPause  = "signal.pause"  // Signal to pause workflow at next step boundary
	SignalResume = "signal.resume" // Signal to resume a paused workflow
)

// RuntimeInjectedInputs is a set of input names that are injected at runtime
// and should not be validated against the workflow's input schema.
// These are internal values used by the workflow engine, not user-provided inputs.
var RuntimeInjectedInputs = map[string]bool{
	"project_path":       true, // Used for preset loading in spawned workflows
	"chat_id":            true, // Injected after validation
	"workflow_id":        true, // Injected after validation
	"unique_activity_id": true, // Injected after validation
	"spawned_by":         true, // Injected after validation
	"__thread":           true, // Internal signal routing key for thread-scoped updates
	"session_daemon_id":  true, // Session-level active daemon for tool execution
}

// NewWorkflowID generates a new random UUID for a root workflow.
// Workflow IDs are always UUIDs - additional context (workflow_name, chat_id, parent_workflow_id)
// should be stored as separate fields in the database, not concatenated into the ID.
func NewWorkflowID() string {
	return uuid.New().String()
}
