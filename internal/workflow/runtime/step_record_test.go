// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A step row is what the builder's Run tab debugs from, so it must record what
// the step was given (its args after {{ }} evaluation), which attempt it was,
// where in the graph it ran, and why it failed.
func TestBuildStepExecution_RecordsWhatTheStepWasGivenAndHowItEnded(t *testing.T) {
	input := types.ActivityInput{
		Runtime: types.RuntimeContext{StepID: "summarize"},
		Node: &reliantv1.Node{
			Id:   "summarize",
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: celLiteral("Summarize issue #42"),
				Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{
					Literal: &reliantv1.ModelSelector{Tags: []string{"flagship"}},
				}},
			}},
		},
	}

	row := buildStepExecution(stepAttempt{
		WorkflowID:   "wf-1",
		StepID:       "summarize",
		ActivityType: "CallLLM",
		Scope:        activityInputInfo{LoopNodeID: "attempt", LoopIteration: 2, NodePath: "attempt.summarize"},
		Attempt:      3,
		Args:         recordedArgs(input),
		Err:          errors.New("provider returned 400: model not found"),
		DurationMs:   120,
	})

	require.True(t, row.InputJSON.Valid, "the resolved args are recorded")
	var args map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(row.InputJSON.String), &args))
	assert.Equal(t, "Summarize issue #42", args["system_prompt"], "a CelString is recorded as the value it resolved to")
	assert.Equal(t, map[string]interface{}{"tags": []interface{}{"flagship"}}, args["model"],
		"an object literal is recorded as the object, not its {literal: …} wrapper")

	assert.Equal(t, "provider returned 400: model not found", row.ErrorMessage.String)
	assert.True(t, row.ErrorMessage.Valid)
	assert.Equal(t, int32(3), row.Attempt.Int32)
	assert.True(t, row.Attempt.Valid)
	assert.Equal(t, "attempt.summarize", row.NodePath.String)
	assert.Equal(t, "attempt", row.LoopNodeID.String)
	assert.Equal(t, int64(2), row.LoopIteration.Int64)
	assert.False(t, row.Success.Bool)
}

func TestBuildStepExecution_SuccessRecordsNoError(t *testing.T) {
	row := buildStepExecution(stepAttempt{
		WorkflowID: "wf-1", StepID: "lint", ActivityType: "ExecuteRunStep",
		Attempt: 1, Output: map[string]interface{}{"exit_code": 0},
	})
	assert.False(t, row.ErrorMessage.Valid)
	assert.False(t, row.InputJSON.Valid, "no node, nothing to record")
	assert.False(t, row.NodePath.Valid)
}

func TestRecordedArgs_OnlyGraphNodes(t *testing.T) {
	assert.False(t, recordedArgs(map[string]interface{}{"chat_id": "c", "error": "x"}).Valid,
		"an infrastructure activity's plain map input is not a node's args")

	save := types.ActivityInput{Node: &reliantv1.Node{Id: "save", Type: "save_message"}}
	assert.False(t, recordedArgs(save).Valid, "a save_message node's args are the message, stored as the message")
}

func TestBoundedJSON_CutsLongStringsAndMarksThem(t *testing.T) {
	long := strings.Repeat("é", 6000) // 12000 bytes
	got := boundedJSON(map[string]interface{}{"content": long, "path": "a.go"}, recordedArgsMaxBytes)
	require.True(t, got.Valid)

	var decoded map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.String), &decoded))
	assert.Equal(t, "a.go", decoded["path"], "short values are kept whole")
	assert.Contains(t, decoded["content"], "… [3952 more characters]")
	assert.True(t, strings.HasPrefix(decoded["content"], strings.Repeat("é", 2048)), "cut on a rune boundary")
}

func TestBoundedJSON_TightensUntilItFits(t *testing.T) {
	items := make([]interface{}, 40)
	for i := range items {
		items[i] = strings.Repeat("x", 3000)
	}
	got := boundedJSON(map[string]interface{}{"calls": items}, recordedArgsMaxBytes)
	require.True(t, got.Valid)
	assert.LessOrEqual(t, len(got.String), recordedArgsMaxBytes)
	assert.Contains(t, got.String, "more characters]")
}

// A {{ }} expression that fails to evaluate is reported through FailStep. Its
// input must name the node the same way a graph activity's runtime context
// does, or the wrapper writes no step row and no node event for it and the
// canvas cannot say which step broke.
func TestFailStepInput_AttributesTheFailureToItsNode(t *testing.T) {
	executor := &StepExecutor{
		chatID:         "chat-1",
		workflowID:     "wf-1",
		loopNodeID:     "attempt",
		loopIteration:  1,
		nodePathPrefix: "attempt",
	}
	node := &reliantv1.Node{Id: "post", Type: "action"}

	// FailStep's input type must carry these keys too, or Temporal drops them
	// before the wrapper sees them: TestFailStepInput_KeepsTheNodeLocation.
	info := extractActivityInputInfo(executor.failStepInput(node, "CEL evaluation failed for step post: boom"))

	assert.Equal(t, "post", info.StepID)
	assert.Equal(t, "chat-1", info.ChatID)
	assert.Equal(t, "wf-1", info.WorkflowID)
	assert.Equal(t, "attempt", info.LoopNodeID)
	assert.Equal(t, 1, info.LoopIteration)
	assert.Equal(t, "attempt.post", info.NodePath)
}
