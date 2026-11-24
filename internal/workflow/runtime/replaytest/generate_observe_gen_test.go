// Copyright (c) 2026 Reliant Labs
//
//go:build replayfixtures

package replaytest

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// observeWorkflowName is the user-draft workflow the workflow_observe
// scenario runs.
const observeWorkflowName = "replay-observe"

// replayObserveWorkflowYAML has one of each node kind the workflow completes
// itself (compute, join, loop, workflow), each with an observe: block, and
// no node that needs a model or a machine. The workflow's own default reaches
// every top-level node; the loop body's tick declares its own.
const replayObserveWorkflowYAML = `name: replay-observe
apiVersion: "0.0.5"
description: |
  Nodes the workflow completes itself, each with an observe block. The replay
  fixtures pin where the run records them (RecordNodeObservation).

entry: [plan, probe]

observe:
  metrics:
    - name: replay.nodes
      kind: counter

nodes:
  - id: plan
    type: compute
    outputs:
      cut: "{{ false }}"
      hold: main_unchanged
    observe:
      events:
        - name: decision
          attributes:
            name: cut
            value: "{{ output.cut ? 'cut' : 'no_cut' }}"
            reason: "{{ output.hold }}"
  - id: probe
    type: compute
    outputs:
      ok: "{{ true }}"
    observe:
      attributes:
        ok: "{{ output.ok }}"
  - id: both
    type: join
    observe:
      attributes:
        sources: "{{ size(output._sources) }}"
  - id: rounds
    type: loop
    while: iter.iteration < 2
    observe:
      attributes:
        iterations: "{{ output._iterations }}"
    inline:
      entry: [tick]
      nodes:
        - id: tick
          type: compute
          outputs:
            i: "{{ iter.iteration }}"
          observe:
            attributes:
              i: "{{ output.i }}"
  - id: child
    type: workflow
    observe:
      attributes:
        result: "{{ output.result }}"
    inline:
      name: child
      entry: [inner]
      outputs:
        result: "{{ nodes.inner.v }}"
      nodes:
        - id: inner
          type: compute
          outputs:
            v: done

edges:
  - from: plan
    default: both
  - from: probe
    default: both
  - from: both
    default: rounds
  - from: rounds
    default: child
`

// TestGenerateFixture_WorkflowObserve pins where a run records the observe
// blocks of the nodes the workflow completes itself: the
// observe-workflow-nodes version marker at the first observed node, then one
// RecordNodeObservation local-activity marker per observed node, in the
// order the workflow completed them. Recorded before that gate existed, the
// same run has no marker of either kind (frozen set
// 2026-11-24-observe-unrecorded).
func TestGenerateFixture_WorkflowObserve(t *testing.T) {
	h := newHarness(t, NewScriptedLLM())
	now := time.Now().UTC()
	require.NoError(t, h.Stack.Repo.CreateWorkflowDraft(h.Ctx, &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     h.UserID,
		Name:       observeWorkflowName,
		Slug:       observeWorkflowName,
		Definition: replayObserveWorkflowYAML,
		Status:     db.WorkflowDraftStatusComplete,
		CreatedAt:  now,
		UpdatedAt:  now,
	}), "create observe workflow draft")
	workflowID := h.StartChat(observeWorkflowName, "Run the graph", nil).WorkflowId

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())

	h.ExportHistory(workflowID, "workflow_observe")
}
