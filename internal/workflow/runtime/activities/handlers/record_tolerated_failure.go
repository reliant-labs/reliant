// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// RecordToleratedFailureActivityName is the activity the workflow runs to put
// on record that a node declaring tolerate_failure failed and the run went on
// (runtime/tolerate_failure.go).
const RecordToleratedFailureActivityName = "RecordToleratedFailure"

// RecordToleratedFailureInput names the node that failed and why. There is no
// chat_id: the run went on past the failure, so it is not reported to the chat
// as an error; it is recorded as the node's failed attempt.
type RecordToleratedFailureInput struct {
	Error string `json:"error"`

	// Where the node is. The handler never reads these; the activity wrapper
	// does, to file the failed attempt (a step row carrying the error) under
	// that node. They must be fields: Temporal decodes the input into this
	// type, and a key it has no field for never reaches the wrapper.
	WorkflowID    string `json:"workflow_id,omitempty" reliant:"-"`
	StepID        string `json:"step_id,omitempty" reliant:"-"`
	LoopNodeID    string `json:"loop_node_id,omitempty" reliant:"-"`
	LoopIteration int    `json:"loop_iteration,omitempty" reliant:"-"`
	NodePath      string `json:"node_path,omitempty" reliant:"-"`
	// Telemetry names the run the node belongs to; the wrapper records its
	// root run id on the attempt row (step_executions.run_id), which is how
	// the run's outcome finds the row as its own.
	Telemetry *types.RunTelemetry `json:"telemetry,omitempty" reliant:"-"`
}

// RecordToleratedFailureOutput is empty: the activity always fails.
type RecordToleratedFailureOutput struct{}

// RecordToleratedFailureActivity records a tolerated node's failure as that
// node's failed attempt. A node's failure is otherwise on record only when an
// activity of the node failed; one decided in the workflow — the
// repeated-tool-error breaker ending an agent, a sub-workflow whose loop
// ended — leaves every attempt successful, and the run would read as having
// succeeded (reliant#2753's run outcome reads the attempts).
//
// It records by failing: the activity wrapper writes the attempt row, with
// the error, for a failed activity, so the row has the shape every reader of
// a failed node already understands. The failure is NON-RETRYABLE (there is
// nothing to retry) and BENIGN (a node the workflow's author made optional
// failed; nobody operating the server can act on it).
type RecordToleratedFailureActivity struct{}

// NewRecordToleratedFailureActivity creates the activity.
func NewRecordToleratedFailureActivity() *RecordToleratedFailureActivity {
	return &RecordToleratedFailureActivity{}
}

func (a *RecordToleratedFailureActivity) Name() string {
	return RecordToleratedFailureActivityName
}

func (a *RecordToleratedFailureActivity) DisplayName() string { return "Tolerated failure" }

func (a *RecordToleratedFailureActivity) Description() string {
	return "Record that a node which tolerates failure failed, and the run went on"
}

func (a *RecordToleratedFailureActivity) Category() schema.ActivityCategory {
	return schema.CategoryUtility
}

// Execute always fails with the node's error.
func (a *RecordToleratedFailureActivity) Execute(_ context.Context, input RecordToleratedFailureInput) (RecordToleratedFailureOutput, error) {
	return RecordToleratedFailureOutput{}, temporal.NewApplicationErrorWithOptions(input.Error, "ToleratedFailure",
		temporal.ApplicationErrorOptions{NonRetryable: true, Category: temporal.ApplicationErrorCategoryBenign})
}
