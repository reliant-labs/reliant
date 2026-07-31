// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// SkippedStepActivity is the emitter of the `skipped` stamp. Everything
// downstream that has to tell "this check never ran" from "this check passed"
// reads that stamp through model.IsSkippedOutput — the step_executions verdict
// most of all, where its absence made every skipped gate a green row.
//
// This pins the emitter's half of that contract. The reader's half lives in
// internal/workflow/runtime/step_execution_row_test.go, which cannot import
// this package without an import cycle.
func TestSkippedStepActivityStampsSkipped(t *testing.T) {
	output, err := NewSkippedStepActivity().Execute(context.Background(), SkippedStepInput{
		WorkflowID: "wf-1",
		StepID:     "review",
		Condition:  "inputs.review_enabled",
	})
	if err != nil {
		t.Fatalf("SkippedStep activity failed: %v", err)
	}

	if !model.IsSkippedOutput(output) {
		t.Fatalf("SkippedStep output %+v is not detected as skipped — a skipped step will be recorded as one that ran", output)
	}
}
