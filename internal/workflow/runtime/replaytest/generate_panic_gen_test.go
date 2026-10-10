// Copyright (c) 2025 Reliant Labs
//
//go:build replayfixtures

package replaytest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/db"
)

// replayPanicWorkflowYAML is one call_llm node at the top of the graph, so
// the injected panic lands on the run's root coroutine.
const replayPanicWorkflowYAML = `name: replay-panic
apiVersion: "0.0.5"
description: |
  One call_llm node. The replay fixtures' panic injector panics where the run
  schedules it.

entry: [work]

inputs:
  model:
    type: model
    default:
      tags: [flagship]

nodes:
  - id: work
    type: call_llm
    args:
      model: "{{inputs.model}}"
`

// startPanicRun saves the replay-panic workflow as the user's draft and
// starts a chat on it, returning the run's workflow id.
func startPanicRun(h *Harness) string {
	h.T.Helper()
	now := time.Now().UTC()
	require.NoError(h.T, h.Stack.Repo.CreateWorkflowDraft(h.Ctx, &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     h.UserID,
		Name:       panicWorkflowName,
		Slug:       panicWorkflowName,
		Definition: replayPanicWorkflowYAML,
		Status:     db.WorkflowDraftStatusComplete,
		CreatedAt:  now,
		UpdatedAt:  now,
	}), "create panic workflow draft")
	return h.StartChat(panicWorkflowName, "Please do the work", nil).WorkflowId
}

// TestGenerateFixture_WorkflowPanic pins a run whose workflow code panics
// (workflowPanicInjector, where the run schedules its first CallLLM): the
// panic is recovered on the root coroutine, the run records the
// workflow-panic-fails-run marker, cancels what it left pending (Cleanup),
// shows the user the panic (WorkflowError), records itself failed
// (WorkflowStatus) and fails — instead of failing its workflow task forever.
func TestGenerateFixture_WorkflowPanic(t *testing.T) {
	h := newHarness(t, NewScriptedLLM())
	workflowID := startPanicRun(h)

	ctx, cancel := context.WithTimeout(h.Ctx, waitTimeout)
	defer cancel()
	runErr := h.Stack.Temporal.GetWorkflow(ctx, workflowID, "").Get(ctx, nil)
	require.Error(t, runErr, "the panicked run must end failed")
	var appErr *temporal.ApplicationError
	require.True(t, errors.As(runErr, &appErr), "want the run's own failure, got %T: %v", runErr, runErr)
	assert.Equal(t, "WorkflowPanic", appErr.Type())
	assert.Contains(t, appErr.Error(), injectedWorkflowPanic)
	h.WaitWorkflowStatus(workflowID, db.Failed())

	h.ExportHistory(workflowID, "workflow_panic")
}
