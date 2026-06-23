// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadTestWorkflow is a helper to parse JSON into a proto workflow for join tests.
func loadTestWorkflow(t *testing.T, wfJSON string) *reliantv1.Workflow {
	t.Helper()
	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)
	return wf
}

func TestJoinState_InitializeJoins(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join",
		"entry": ["started"],
		"nodes": [
			{"id": "started", "type": "noop"},
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"},
			{"id": "sync", "type": "join", "condition": "all"},
			{"id": "next", "type": "do_next"}
		],
		"edges": [
			{"from": "started", "default": "task_a"},
			{"from": "started", "default": "task_b"},
			{"from": "task_a", "default": "sync"},
			{"from": "task_b", "default": "sync"},
			{"from": "sync", "default": "next"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)

	// Should have one join step initialized
	require.Len(t, js.Progress, 1)
	require.Contains(t, js.Progress, "sync")

	// Should have correct sources from incoming edges
	progress := js.Progress["sync"]
	assert.ElementsMatch(t, []string{"task_a", "task_b"}, progress.Sources)
	assert.Empty(t, progress.Completed)
	assert.False(t, progress.Triggered)
}

func TestJoinState_RecordCompletion(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"},
			{"id": "sync", "type": "join", "condition": "all"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"},
			{"from": "task_b", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)

	// Record first completion
	affected := js.RecordCompletion("task_a", map[string]interface{}{"result": "a"}, time.Now())
	assert.Contains(t, affected, "sync")

	progress := js.Progress["sync"]
	assert.True(t, progress.Completed["task_a"])
	assert.False(t, progress.Completed["task_b"])
	assert.Equal(t, map[string]interface{}{"result": "a"}, progress.Results["task_a"])

	// Record second completion
	affected = js.RecordCompletion("task_b", map[string]interface{}{"result": "b"}, time.Now())
	assert.Contains(t, affected, "sync")

	assert.True(t, progress.Completed["task_a"])
	assert.True(t, progress.Completed["task_b"])
}

func TestJoinState_IsJoinSatisfied_All(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"},
			{"id": "sync", "type": "join", "condition": "all"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"},
			{"from": "task_b", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)

	// Not satisfied with no completions
	assert.False(t, js.IsJoinSatisfied("sync", "all"))

	// Not satisfied with one completion
	js.RecordCompletion("task_a", nil, time.Now())
	assert.False(t, js.IsJoinSatisfied("sync", "all"))

	// Satisfied with all completions
	js.RecordCompletion("task_b", nil, time.Now())
	assert.True(t, js.IsJoinSatisfied("sync", "all"))
}

func TestJoinState_IsJoinSatisfied_Any(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"},
			{"id": "sync", "type": "join", "condition": "any"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"},
			{"from": "task_b", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)

	// Not satisfied with no completions
	assert.False(t, js.IsJoinSatisfied("sync", "any"))

	// Satisfied with one completion
	js.RecordCompletion("task_a", nil, time.Now())
	assert.True(t, js.IsJoinSatisfied("sync", "any"))
}

func TestJoinState_MarkTriggered(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "sync", "type": "join", "condition": "all"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)

	js.RecordCompletion("task_a", nil, time.Now())
	assert.True(t, js.IsJoinSatisfied("sync", "all"))

	// Mark as triggered
	js.MarkTriggered("sync")

	// Should no longer be satisfied (already triggered)
	assert.False(t, js.IsJoinSatisfied("sync", "all"))
}

func TestJoinState_GetJoinOutput(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"},
			{"id": "sync", "type": "join", "condition": "all"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"},
			{"from": "task_b", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)

	js.RecordCompletion("task_a", map[string]interface{}{"value": 1}, time.Now())
	js.RecordCompletion("task_b", map[string]interface{}{"value": 2}, time.Now())

	output := js.GetJoinOutput("sync")

	// Check canonical join output shape
	sources, ok := output[model.JoinOutputSourcesField].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, sources, 2)

	sourceByID := make(map[string]map[string]interface{}, len(sources))
	for _, source := range sources {
		sourceID, _ := source["id"].(string)
		sourceByID[sourceID] = source
	}

	require.Contains(t, sourceByID, "task_a")
	require.Contains(t, sourceByID, "task_b")
	assert.Equal(t, "completed", sourceByID["task_a"]["status"])
	assert.Equal(t, "completed", sourceByID["task_b"]["status"])
}

func TestJoinState_DuplicateEdges(t *testing.T) {
	// Test that duplicate edges to same join don't create duplicate sources
	workflow := loadTestWorkflow(t, `{
		"name": "test-join",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "sync", "type": "join", "condition": "all"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"},
			{"from": "task_a", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)

	// Should only have one source despite two edges
	assert.Len(t, js.Progress["sync"].Sources, 1)
	assert.Equal(t, "task_a", js.Progress["sync"].Sources[0])
}

func TestJoinState_NonJoinStep(t *testing.T) {
	// Test that non-join steps don't create progress entries
	workflow := loadTestWorkflow(t, `{
		"name": "test-no-join",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"}
		],
		"edges": [
			{"from": "task_a", "default": "task_b"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)

	assert.Empty(t, js.Progress)
}

// testLogger implements joinLogger for testing
type testLogger struct {
	messages []string
}

func (l *testLogger) Info(msg string, keyvals ...interface{}) {
	l.messages = append(l.messages, msg)
}

func TestProcessJoinEvents_ConditionAll(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"},
			{"id": "sync", "type": "join", "condition": "all"},
			{"id": "next", "type": "do_next"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"},
			{"from": "task_b", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)
	logger := &testLogger{}
	nodeOutputs := make(map[string]interface{})

	// First event - task_a completes
	events := []*core.WorkflowEvent{
		{ID: "e1", StepID: "task_a", Data: map[string]interface{}{"result": "a"}},
	}

	result := processJoinEvents(events, js, workflow, "wf1", "chat1", "test", nodeOutputs, logger, nil, time.Now())

	// Should still have original event, no join event yet
	assert.Len(t, result, 1)
	assert.Equal(t, "task_a", result[0].StepID)

	// Second event - task_b completes
	events = []*core.WorkflowEvent{
		{ID: "e2", StepID: "task_b", Data: map[string]interface{}{"result": "b"}},
	}

	result = processJoinEvents(events, js, workflow, "wf1", "chat1", "test", nodeOutputs, logger, nil, time.Now())

	// Should have original event + join completion event
	assert.Len(t, result, 2)
	assert.Equal(t, "task_b", result[0].StepID)
	assert.Equal(t, "sync", result[1].StepID)

	// Verify join output
	joinData := result[1].Data
	sources, ok := joinData[model.JoinOutputSourcesField].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, sources, 2)

	sourceIDs := make([]string, 0, len(sources))
	for _, source := range sources {
		if sourceID, ok := source["id"].(string); ok {
			sourceIDs = append(sourceIDs, sourceID)
		}
	}
	assert.ElementsMatch(t, []string{"task_a", "task_b"}, sourceIDs)

	// Verify nodeOutputs was updated in flattened/canonical join form
	assert.Contains(t, nodeOutputs, "sync")
	nodeJoinOutput, ok := nodeOutputs["sync"].(map[string]interface{})
	require.True(t, ok)
	_, hasSources := nodeJoinOutput[model.JoinOutputSourcesField]
	assert.True(t, hasSources)
}

func TestProcessJoinEvents_ConditionAny(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"},
			{"id": "sync", "type": "join", "condition": "any"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"},
			{"from": "task_b", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)
	logger := &testLogger{}
	nodeOutputs := make(map[string]interface{})

	// First event triggers join immediately with condition="any"
	events := []*core.WorkflowEvent{
		{ID: "e1", StepID: "task_a", Data: map[string]interface{}{"result": "a"}},
	}

	result := processJoinEvents(events, js, workflow, "wf1", "chat1", "test", nodeOutputs, logger, nil, time.Now())

	// Should have original event + join completion event
	assert.Len(t, result, 2)
	assert.Equal(t, "task_a", result[0].StepID)
	assert.Equal(t, "sync", result[1].StepID)

	// Second event should NOT trigger join again
	events = []*core.WorkflowEvent{
		{ID: "e2", StepID: "task_b", Data: map[string]interface{}{"result": "b"}},
	}

	result = processJoinEvents(events, js, workflow, "wf1", "chat1", "test", nodeOutputs, logger, nil, time.Now())

	// Should only have original event, join already triggered
	assert.Len(t, result, 1)
	assert.Equal(t, "task_b", result[0].StepID)
}

func TestProcessJoinEvents_SkipsWorkflowStartEvent(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "sync", "type": "join", "condition": "all"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)
	logger := &testLogger{}
	nodeOutputs := make(map[string]interface{})

	// Workflow start event (empty StepID) should be skipped
	events := []*core.WorkflowEvent{
		{ID: "start", StepID: "", Data: map[string]interface{}{}},
	}

	result := processJoinEvents(events, js, workflow, "wf1", "chat1", "test", nodeOutputs, logger, nil, time.Now())

	// Should only have original start event, no join processing
	assert.Len(t, result, 1)
	assert.Equal(t, "", result[0].StepID)
}

func TestExpandJoinCondition(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"all expands to CEL", "all", "sources.all(s, s.status == 'completed')"},
		{"any expands to CEL", "any", "sources.exists(s, s.status == 'completed')"},
		{"numeric defaults to all", "2", "sources.all(s, s.status == 'completed')"},
		{"empty defaults to all", "", "sources.all(s, s.status == 'completed')"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := expandJoinCondition(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestJoinState_SkippedSourcesSatisfyAllCondition(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join-skipped",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"},
			{"id": "sync", "type": "join", "condition": "all"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"},
			{"from": "task_b", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)

	// task_a completes normally
	js.RecordCompletion("task_a", map[string]interface{}{"result": "a"}, time.Now())
	assert.False(t, js.IsJoinSatisfied("sync", "all"))

	// task_b is skipped (using SkippedOutput)
	skippedOutput := model.SkippedOutputMap()
	js.RecordCompletion("task_b", skippedOutput, time.Now())

	// Skipped sources should satisfy "all" condition
	assert.True(t, js.IsJoinSatisfied("sync", "all"))
}

func TestJoinState_SkippedSourcesSatisfyAnyCondition(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join-skipped-any",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"},
			{"id": "sync", "type": "join", "condition": "any"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"},
			{"from": "task_b", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)

	// task_a is skipped (using SkippedOutput)
	skippedOutput := model.SkippedOutputMap()
	js.RecordCompletion("task_a", skippedOutput, time.Now())

	// Skipped sources should satisfy "any" condition (since skipped = completed)
	assert.True(t, js.IsJoinSatisfied("sync", "any"))
}

func TestBuildJoinSources_SkippedTreatedAsCompleted(t *testing.T) {
	progress := &JoinProgress{
		Sources:   []string{"task_a", "task_b", "task_c"},
		Completed: map[string]bool{"task_a": true},
		Skipped:   map[string]bool{"task_b": true},
		Results: map[string]interface{}{
			"task_a": map[string]interface{}{"value": 1},
			"task_b": model.SkippedOutputMap(),
		},
	}

	sources := progress.BuildJoinSources()
	require.Len(t, sources, 3)

	// Find each source by ID
	sourceMap := make(map[string]map[string]interface{})
	for _, source := range sources {
		sourceID, ok := source["id"].(string)
		require.True(t, ok)
		sourceMap[sourceID] = source
	}

	// task_a is completed
	assert.Equal(t, "completed", sourceMap["task_a"]["status"])
	assert.Equal(t, true, sourceMap["task_a"]["completed"])

	// task_b is skipped but should appear as completed
	assert.Equal(t, "completed", sourceMap["task_b"]["status"])
	assert.Equal(t, true, sourceMap["task_b"]["completed"])
	assert.NotNil(t, sourceMap["task_b"]["output"])

	// task_c is still pending
	assert.Equal(t, "pending", sourceMap["task_c"]["status"])
	assert.Equal(t, false, sourceMap["task_c"]["completed"])
}

func TestProcessJoinEvents_SkippedSourceTriggersJoin(t *testing.T) {
	workflow := loadTestWorkflow(t, `{
		"name": "test-join-skipped-event",
		"entry": ["task_a"],
		"nodes": [
			{"id": "task_a", "type": "do_a"},
			{"id": "task_b", "type": "do_b"},
			{"id": "sync", "type": "join", "condition": "all"}
		],
		"edges": [
			{"from": "task_a", "default": "sync"},
			{"from": "task_b", "default": "sync"}
		]
	}`)

	js := NewJoinState()
	js.InitializeJoins(workflow)
	logger := &testLogger{}
	nodeOutputs := make(map[string]interface{})

	// task_a completes normally
	events := []*core.WorkflowEvent{
		{ID: "e1", StepID: "task_a", Data: map[string]interface{}{"result": "a"}},
	}
	result := processJoinEvents(events, js, workflow, "wf1", "chat1", "test", nodeOutputs, logger, nil, time.Now())
	assert.Len(t, result, 1) // No join event yet

	// task_b is skipped - use map representation that IsSkippedOutput can detect
	events = []*core.WorkflowEvent{
		{ID: "e2", StepID: "task_b", Data: map[string]interface{}{"skipped": true}},
	}
	result = processJoinEvents(events, js, workflow, "wf1", "chat1", "test", nodeOutputs, logger, nil, time.Now())

	// Should have original event + join completion event (skipped satisfies "all")
	assert.Len(t, result, 2)
	assert.Equal(t, "task_b", result[0].StepID)
	assert.Equal(t, "sync", result[1].StepID)
}
