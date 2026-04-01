// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// SINGLE ENTRY POINT TESTS
// ============================================================================

func TestEntryExecution_SingleEntry(t *testing.T) {
	t.Run("workflow with single entry triggers correct node on start", func(t *testing.T) {
		wfJSON := `{
			"name": "test-workflow",
			"entry": ["node_a"],
			"nodes": [
				{"id": "node_a", "type": "test_action"},
				{"id": "node_b", "type": "other_action"}
			],
			"edges": [
				{"from": "node_a", "default": "node_b"}
			]
		}`
		wf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		// Verify GetEntryNodes returns correct node
		entryNodes := model.GetEntryNodes(wf)
		assert.Equal(t, []string{"node_a"}, entryNodes)

		// Verify HasExplicitEntry returns true
		assert.True(t, len(wf.GetEntry()) > 0)

		// Verify FindTriggeredNodes returns entry node on workflow start
		sm := NewSimplifiedStateMachine("test-wf-id", wf)
		startEvent := &core.WorkflowEvent{
			ID:         "start-event",
			WorkflowID: "test-wf-id",
			StepID:     "", // Empty = workflow start
			Data:       map[string]interface{}{},
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{startEvent}, map[string]interface{}{}, map[string]interface{}{})
		require.NoError(t, err)
		require.Len(t, triggered, 1)
		assert.Equal(t, "node_a", triggered[0].Node.GetId())
	})

	t.Run("entry field from JSON parsing", func(t *testing.T) {
		wfJSON := `{
			"name": "json-workflow",
			"entry": ["start_node"],
			"nodes": [
				{"id": "start_node", "type": "start_action"}
			],
			"edges": []
		}`

		wf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		assert.True(t, len(wf.GetEntry()) > 0)
		assert.Equal(t, []string{"start_node"}, model.GetEntryNodes(wf))
	})

	t.Run("entry field from YAML parsing via LoadWorkflow", func(t *testing.T) {
		wfJSON := `{
			"name": "yaml-workflow",
			"entry": ["main_node"],
			"nodes": [
				{"id": "main_node", "type": "main_action"}
			],
			"edges": []
		}`

		wf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		assert.True(t, len(wf.GetEntry()) > 0)
		assert.Equal(t, []string{"main_node"}, model.GetEntryNodes(wf))
	})
}

// ============================================================================
// MULTIPLE ENTRY POINTS (PARALLEL START) TESTS
// ============================================================================

func TestEntryExecution_MultipleEntryPoints(t *testing.T) {
	t.Run("workflow with multiple entries triggers all nodes in parallel", func(t *testing.T) {
		wfJSON := `{
			"name": "parallel-start-workflow",
			"entry": ["node_a", "node_b"],
			"nodes": [
				{"id": "node_a", "type": "action_a"},
				{"id": "node_b", "type": "action_b"},
				{"id": "join_node", "type": "join", "condition": "all"}
			],
			"edges": [
				{"from": "node_a", "default": "join_node"},
				{"from": "node_b", "default": "join_node"}
			]
		}`
		wf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		// Verify GetEntryNodes returns both nodes
		entryNodes := model.GetEntryNodes(wf)
		assert.ElementsMatch(t, []string{"node_a", "node_b"}, entryNodes)

		// Verify HasExplicitEntry returns true
		assert.True(t, len(wf.GetEntry()) > 0)

		// Verify FindTriggeredNodes returns both entry nodes on workflow start
		sm := NewSimplifiedStateMachine("test-wf-id", wf)
		startEvent := &core.WorkflowEvent{
			ID:         "start-event",
			WorkflowID: "test-wf-id",
			StepID:     "", // Empty = workflow start
			Data:       map[string]interface{}{},
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{startEvent}, map[string]interface{}{}, map[string]interface{}{})
		require.NoError(t, err)
		require.Len(t, triggered, 2)

		triggeredIDs := []string{triggered[0].Node.GetId(), triggered[1].Node.GetId()}
		assert.ElementsMatch(t, []string{"node_a", "node_b"}, triggeredIDs)
	})

	t.Run("entry array from JSON parsing", func(t *testing.T) {
		wfJSON := `{
			"name": "json-parallel-workflow",
			"entry": ["racer_1", "racer_2", "racer_3"],
			"nodes": [
				{"id": "racer_1", "type": "race"},
				{"id": "racer_2", "type": "race"},
				{"id": "racer_3", "type": "race"}
			],
			"edges": []
		}`

		wf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		assert.True(t, len(wf.GetEntry()) > 0)
		entryNodes := model.GetEntryNodes(wf)
		assert.ElementsMatch(t, []string{"racer_1", "racer_2", "racer_3"}, entryNodes)
	})
}

// ============================================================================
// ENTRY FIELD HELPERS TESTS
// ============================================================================

func TestEntryExecution_EntryFieldHelpers(t *testing.T) {
	t.Run("workflow without entry field", func(t *testing.T) {
		wfJSON := `{
			"name": "missing-entry-workflow",
			"nodes": [
				{"id": "node_a", "type": "action_a"}
			],
			"edges": []
		}`

		wf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		// HasExplicitEntry should return false
		assert.False(t, len(wf.GetEntry()) > 0)

		// GetEntryNodes should return nil
		assert.Nil(t, model.GetEntryNodes(wf))
	})

	t.Run("workflow with empty entry", func(t *testing.T) {
		wfJSON := `{
			"name": "nil-entry-workflow",
			"nodes": [
				{"id": "node_a", "type": "action_a"}
			],
			"edges": []
		}`

		wf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		assert.False(t, len(wf.GetEntry()) > 0)
		assert.Nil(t, model.GetEntryNodes(wf))
	})
}

// ============================================================================
// EDGE CASES AND SPECIAL SCENARIOS
// ============================================================================

func TestEntryExecution_EdgeCases(t *testing.T) {
	t.Run("GetEntryNodes handles different Entry values", func(t *testing.T) {
		// Test single entry
		wf1JSON := `{"name":"t1","entry": ["single_node"],"nodes":[{"id":"single_node","type":"a"}],"edges":[]}`
		wf1, err := LoadWorkflow([]byte(wf1JSON))
		require.NoError(t, err)
		assert.Equal(t, []string{"single_node"}, model.GetEntryNodes(wf1))

		// Test multiple entries
		wf2JSON := `{"name":"t2","entry":["a","b"],"nodes":[{"id":"a","type":"a"},{"id":"b","type":"b"}],"edges":[]}`
		wf2, err := LoadWorkflow([]byte(wf2JSON))
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"a", "b"}, model.GetEntryNodes(wf2))

		// Test nil (no entry)
		wf3JSON := `{"name":"t3","nodes":[{"id":"a","type":"a"}],"edges":[]}`
		wf3, err := LoadWorkflow([]byte(wf3JSON))
		require.NoError(t, err)
		assert.Nil(t, model.GetEntryNodes(wf3))
	})

	t.Run("FindTriggeredNodes does not trigger entry nodes for non-start events", func(t *testing.T) {
		wfJSON := `{
			"name": "test-workflow",
			"entry": ["node_a"],
			"nodes": [
				{"id": "node_a", "type": "action_a"},
				{"id": "node_b", "type": "action_b"}
			],
			"edges": [
				{"from": "node_a", "default": "node_b"}
			]
		}`
		wf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		sm := NewSimplifiedStateMachine("test-wf-id", wf)

		// Event from node_a completion (not a start event)
		completionEvent := &core.WorkflowEvent{
			ID:         "completion-event",
			WorkflowID: "test-wf-id",
			StepID:     "node_a", // Non-empty = step completion
			Data:       map[string]interface{}{},
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{completionEvent}, map[string]interface{}{}, map[string]interface{}{})
		require.NoError(t, err)
		require.Len(t, triggered, 1)
		assert.Equal(t, "node_b", triggered[0].Node.GetId()) // Should trigger node_b via edge, not re-trigger entry
	})

	t.Run("single-node workflow with entry and no edges", func(t *testing.T) {
		wfJSON := `{
			"name": "single-node-workflow",
			"entry": ["only_node"],
			"nodes": [
				{"id": "only_node", "type": "solo_action"}
			],
			"edges": []
		}`
		wf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		sm := NewSimplifiedStateMachine("test-wf-id", wf)
		startEvent := &core.WorkflowEvent{
			ID:         "start-event",
			WorkflowID: "test-wf-id",
			StepID:     "",
			Data:       map[string]interface{}{},
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{startEvent}, map[string]interface{}{}, map[string]interface{}{})
		require.NoError(t, err)
		require.Len(t, triggered, 1)
		assert.Equal(t, "only_node", triggered[0].Node.GetId())
	})

	// NOTE: Inline loop test with v3 LoopNode removed — will be ported with Phase 4D (simulator port)
}
