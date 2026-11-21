// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// Every node attempt records the run it belongs to (step_executions.run_id),
// read from the activity's RuntimeContext.Telemetry: the root run's id, which
// a spawned sub-agent names as its RootRunID. The root workflow id is reused
// by every run of a chat, so this is what keeps an earlier run's failed
// attempt out of the next run's outcome (run_outcome.go).
func TestStepExecutionRecordsTheRunItBelongsTo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		tel  *types.RunTelemetry
		want string
	}{
		{"a root run's node", &types.RunTelemetry{RunID: "run-root", RootRunID: "run-root"}, "run-root"},
		{"a sub-agent's node is its root run's", &types.RunTelemetry{RunID: "wf-child", ParentRunID: "run-root", RootRunID: "run-root"}, "run-root"},
		{"telemetry with no root names its own run", &types.RunTelemetry{RunID: "run-own"}, "run-own"},
		{"no telemetry names no run", nil, ""},
	} {
		input := types.ActivityInput{Runtime: types.RuntimeContext{
			ChatID: "chat", WorkflowID: "wf", StepID: "post", NodePath: "post", Telemetry: tc.tel,
		}}
		info := extractActivityInputInfo(input)
		if info.RunID != tc.want {
			t.Errorf("%s: extractActivityInputInfo().RunID = %q, want %q", tc.name, info.RunID, tc.want)
		}
		exec := buildStepExecution(stepAttempt{WorkflowID: "wf", StepID: "post", ActivityType: "Action", Scope: info})
		var got string
		if exec.RunID != nil {
			got = *exec.RunID
		}
		if got != tc.want {
			t.Errorf("%s: the step row's run_id = %q, want %q", tc.name, got, tc.want)
		}
		if tc.want == "" && exec.RunID != nil {
			t.Errorf("%s: a step that names no run must record NULL, not %q", tc.name, *exec.RunID)
		}
	}

	// FailStep's input (a {{ }} expression that did not evaluate) names the
	// run too: its row is the node's failed attempt, and without the run id
	// a tolerated one would be read as the next run's on the reused
	// workflow id (reliant#2863 R2.B2).
	executor := &StepExecutor{
		chatID: "chat", workflowID: "wf",
		execContext: &ExecutionContext{Telemetry: &types.RunTelemetry{RunID: "run-root", RootRunID: "run-root"}},
	}
	failInfo := extractActivityInputInfo(executor.failStepInput(&reliantv1.Node{Id: "post", Type: "action"}, "boom"))
	if failInfo.RunID != "run-root" {
		t.Errorf("FailStep: extractActivityInputInfo().RunID = %q, want run-root", failInfo.RunID)
	}
	if exec := buildStepExecution(stepAttempt{WorkflowID: "wf", StepID: "post", ActivityType: "FailStep", Scope: failInfo}); exec.RunID == nil || *exec.RunID != "run-root" {
		t.Errorf("FailStep: the step row's run_id = %v, want run-root", exec.RunID)
	}

	// A flat input (not v3) carrying its telemetry at the top level.
	flat := map[string]interface{}{"workflow_id": "wf", "step_id": "s", "telemetry": map[string]interface{}{"run_id": "run-flat"}}
	if got := extractActivityInputInfo(flat).RunID; got != "run-flat" {
		t.Errorf("flat input: RunID = %q, want run-flat", got)
	}
}
