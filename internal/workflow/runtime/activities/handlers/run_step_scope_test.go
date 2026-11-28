// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The workflow sends ExecuteRunStep a flat map (StepExecutor.startRun), and
// Temporal decodes it into ExecuteRunStepInput before the ActivityWrapper
// sees it. The wrapper writes the step's row from that typed input
// (extractActivityInputInfo marshals it back to JSON), so a key the struct
// does not declare never reaches step_executions. node_path and telemetry
// were such keys: every run node in a loop body was listed by get_run under
// its bare id ("run", not "gate.run"), which a run watchdog could not find in
// the workflow graph, and with no run id.
func TestExecuteRunStepInput_KeepsTheScopeItsStepRowRecords(t *testing.T) {
	t.Parallel()

	// The keys startRun sets for a run node in iteration 14 of loop gate.
	sent := map[string]any{
		"command":        "true",
		"workflow_id":    "wf-1",
		"chat_id":        "chat-1",
		"step_id":        "full",
		"node_path":      "gate.full",
		"node_type":      "run",
		"loop_node_id":   "gate",
		"loop_iteration": 14,
		"telemetry":      map[string]any{"run_id": "run-1", "workflow_name": "cp-merge"},
	}
	raw, err := json.Marshal(sent)
	require.NoError(t, err)

	var input ExecuteRunStepInput
	require.NoError(t, json.Unmarshal(raw, &input))

	back, err := json.Marshal(input)
	require.NoError(t, err)
	var seen map[string]any
	require.NoError(t, json.Unmarshal(back, &seen))

	assert.Equal(t, "full", seen["step_id"])
	assert.Equal(t, "gate.full", seen["node_path"], "the row would record the bare id, not the node's path")
	assert.Equal(t, "gate", seen["loop_node_id"])
	assert.EqualValues(t, 14, seen["loop_iteration"])
	tel, _ := seen["telemetry"].(map[string]any)
	require.NotNil(t, tel, "the row would record no run id")
	assert.Equal(t, "run-1", tel["run_id"])
}
