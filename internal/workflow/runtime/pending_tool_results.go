// Copyright (c) 2025 Reliant Labs
package runtime

import (
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"go.temporal.io/sdk/workflow"
)

// PendingToolResultGroup collects tool results from all members of a grouped
// execute_tools step (regular tools activity + spawn inline workflows).
// It owns a workflow.Future that resolves with the combined result when every
// member has reported in. The RunningStep uses this future directly, so
// HandleCompletion fires naturally with the full data.
type PendingToolResultGroup struct {
	// StepID is the execute_tools node ID that triggered this group.
	StepID string

	// Node and Event from the triggering step, used to build the completion event.
	Node  *reliantv1.Node
	Event *core.WorkflowEvent

	// ExpectedCount is the total number of members in this group
	// (1 regular-tools activity if present + N spawn inline workflows).
	ExpectedCount int

	// Results collects individual tool result maps as they complete.
	// Each entry is a single tool result: {"tool_call_id":..., "content":..., "is_error":...}.
	Results []interface{}

	// MessageOutput is the "message" field from the regular tools activity result.
	// Only one member (the regular tools activity) sets this.
	MessageOutput map[string]interface{}

	// CompletedCount tracks how many members have reported results.
	CompletedCount int

	// Future resolves with BuildCombinedResult() when all members complete.
	// This is the future used by the RunningStep.
	Future workflow.Future

	// settable is the write-end of Future. Set automatically when IsComplete().
	settable workflow.Settable
}

// NewPendingToolResultGroup creates a group with an owned Future/Settable pair.
func NewPendingToolResultGroup(ctx workflow.Context, stepID string, node *reliantv1.Node, event *core.WorkflowEvent, expectedCount int) *PendingToolResultGroup {
	f, s := workflow.NewFuture(ctx)
	return &PendingToolResultGroup{
		StepID:        stepID,
		Node:          node,
		Event:         event,
		ExpectedCount: expectedCount,
		Future:        f,
		settable:      s,
	}
}

// IsComplete returns true when all expected members have reported.
func (g *PendingToolResultGroup) IsComplete() bool {
	return g.CompletedCount >= g.ExpectedCount
}

// AddToolResults appends one or more tool result entries and increments the completed count.
// If the group becomes complete, the future is resolved automatically.
func (g *PendingToolResultGroup) AddToolResults(results ...interface{}) {
	g.Results = append(g.Results, results...)
	g.CompletedCount++
	g.resolveIfComplete()
}

// AddToolResultWithMessage appends tool results and stores the message output.
// Used by the regular tools activity path which produces both tool_results and a message.
// If the group becomes complete, the future is resolved automatically.
func (g *PendingToolResultGroup) AddToolResultWithMessage(results []interface{}, message map[string]interface{}) {
	g.Results = append(g.Results, results...)
	if message != nil {
		g.MessageOutput = message
	}
	g.CompletedCount++
	g.resolveIfComplete()
}

// BuildCombinedResult constructs the final tool result in ExecuteToolsOutput format.
// This matches the shape produced by buildFinalToolResult.
func (g *PendingToolResultGroup) BuildCombinedResult() map[string]interface{} {
	return buildFinalToolResult(g.Results, g.MessageOutput)
}

// resolveIfComplete checks completion and resolves the future if done.
func (g *PendingToolResultGroup) resolveIfComplete() {
	if g.IsComplete() {
		g.settable.SetValue(g.BuildCombinedResult())
	}
}
