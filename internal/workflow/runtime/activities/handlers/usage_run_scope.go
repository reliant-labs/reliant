// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// usageRun is the run a usage record names (agent-analytics.md §3.2): the
// columns llm_call_usage and tool_call_usage share, so a model call and a tool
// call join on them.
type usageRun struct {
	// WorkflowID is the Temporal workflow id.
	WorkflowID   string
	ThreadID     string
	WorkflowName string
	NodeID       string
	Preset       string
	// AIRunID and AIRootRunID are forge.ai.run.id and forge.ai.root_run.id,
	// which Temporal's run id is not.
	AIRunID     string
	AIRootRunID string
}

// usageRunOf is the run of the activity ctx belongs to, from the scope the
// worker's run-scope interceptor (and the agent turn, for its preset) put on
// ctx: the scope the record's span is opened from, so the row and the span
// name one run. A scope with no workflow takes the activity's; a top-level
// run is its own root.
func usageRunOf(ctx context.Context) usageRun {
	scope := telemetry.RunScopeFromContext(ctx)
	run := usageRun{
		WorkflowID:   scope.WorkflowID,
		ThreadID:     scope.ThreadID,
		WorkflowName: scope.WorkflowName,
		NodeID:       scope.NodeID,
		Preset:       scope.Preset,
		AIRunID:      scope.RunID,
		AIRootRunID:  scope.RootRunID,
	}
	if run.WorkflowID == "" && activity.IsActivity(ctx) {
		run.WorkflowID = activity.GetInfo(ctx).WorkflowExecution.ID
	}
	if run.AIRootRunID == "" {
		run.AIRootRunID = run.AIRunID
	}
	return run
}
