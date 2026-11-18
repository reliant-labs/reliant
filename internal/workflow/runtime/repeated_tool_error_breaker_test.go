// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// failingBatch is an execute_tools completion whose one call failed with msg.
func failingBatch(callID, msg string) *StepEvent {
	return &StepEvent{Data: map[string]interface{}{"tool_results": []interface{}{
		map[string]interface{}{"tool_call_id": callID, "name": "github__repo_get_tree", "is_error": true, "content": msg},
	}}}
}

// reliant#2752: pr-review's cp_reader asked for three DIFFERENT paths, each
// answered "not found", and the breaker ended the agent as though it had
// retried one call three times. The input is part of what repeats.
func TestRepeatedToolErrorBreaker_DifferentInputsAreNotARepeat(t *testing.T) {
	t.Parallel()
	b := NewRepeatedToolErrorBreaker(RepeatedToolErrorLimit)
	for i, path := range []string{"cmd/a", "cmd/b", "cmd/c", "cmd/d"} {
		id := "tc-" + path
		err := b.Observe("reader", executeToolsActivityName, failingBatch(id, "GitHub returned not found"),
			map[string]string{id: `{"path":"` + path + `"}`}, func() bool { return true })
		assert.NoError(t, err, "call %d explored a new path; it is not a retry loop", i+1)
	}
}

// The same call failing the same way is still a retry loop, and trips.
func TestRepeatedToolErrorBreaker_SameInputTrips(t *testing.T) {
	t.Parallel()
	b := NewRepeatedToolErrorBreaker(RepeatedToolErrorLimit)
	var err error
	for i := 0; i < RepeatedToolErrorLimit; i++ {
		id := "tc-" + string(rune('a'+i)) // a model mints a new id per call
		err = b.Observe("reader", executeToolsActivityName, failingBatch(id, "GitHub returned not found"),
			map[string]string{id: `{"path":"cmd/a"}`}, func() bool { return true })
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed 3 times in a row with the same error")
}

// A history recorded before the input counted tripped on the input-blind
// signature: replayed, it must trip at the same step, so the version gate
// answers "not input-aware" there.
func TestRepeatedToolErrorBreaker_OldHistoriesTripAsTheyDid(t *testing.T) {
	t.Parallel()
	b := NewRepeatedToolErrorBreaker(RepeatedToolErrorLimit)
	asked := 0
	var err error
	for i, path := range []string{"cmd/a", "cmd/b", "cmd/c"} {
		id := "tc-" + path
		err = b.Observe("reader", executeToolsActivityName, failingBatch(id, "GitHub returned not found"),
			map[string]string{id: `{"path":"` + path + `"}`}, func() bool { asked++; return false })
		if i < 2 {
			require.NoError(t, err)
		}
	}
	require.Error(t, err, "input-blind, three identical errors trip")
	assert.Equal(t, 1, asked, "the gate is consulted only where the two signatures disagree")
}

func TestToolCallInputs(t *testing.T) {
	t.Parallel()
	node := &reliantv1.Node{Args: &reliantv1.Node_ExecuteTools{ExecuteTools: &reliantv1.ExecuteToolsArgs{
		ResolvedToolCalls: []*reliantv1.ToolCallMsg{{Id: "a", Name: "view", Input: `{"path":"x"}`}},
	}}}
	assert.Equal(t, map[string]string{"a": `{"path":"x"}`}, toolCallInputs(node))
	assert.Nil(t, toolCallInputs(&reliantv1.Node{}))
}
