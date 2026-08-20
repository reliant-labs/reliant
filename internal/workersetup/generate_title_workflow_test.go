// Copyright (c) 2025 Reliant Labs
//
// Tests for GenerateTitleWorkflow's retry-then-fallback policy.
//
// The fallback (title = truncated first message) used to live inside the
// activity, which swallowed the LLM error and returned success. A provider
// outage was therefore indistinguishable from a working system: no retry, no
// alert, and every chat titled with the user's own text. These tests pin the
// replacement contract — the LLM is retried, and the fallback happens once,
// last, and only after the retries are spent.
package workersetup

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// titleInput is the input CreateChat sends.
func titleInput() map[string]interface{} {
	return map[string]interface{}{
		"chat_id":       "chat-123",
		"first_message": "creating titles of chats is broken",
	}
}

// usedFallback reports whether an activity invocation asked for the fallback.
func usedFallback(input map[string]interface{}) bool {
	v, _ := input["use_fallback"].(bool)
	return v
}

// generateTitleStub stands in for the real GenerateTitle activity. The
// workflow dispatches by NAME, so the test environment needs something
// registered under that name for OnActivity to intercept.
func generateTitleStub(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
	return nil, nil
}

// newTitleEnv returns an environment with the activity registered and a
// recorder for the inputs each invocation received.
func newTitleEnv(t *testing.T, handler func(input map[string]interface{}, call int) (map[string]interface{}, error)) (*testsuite.TestWorkflowEnvironment, *[]map[string]interface{}) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(generateTitleStub, activity.RegisterOptions{Name: "GenerateTitle"})

	calls := &[]map[string]interface{}{}
	env.OnActivity("GenerateTitle", mock.Anything, mock.Anything).Return(
		func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			*calls = append(*calls, input)
			return handler(input, len(*calls))
		},
	)
	return env, calls
}

// A transient failure must be retried, not immediately downgraded to the
// fallback. This is the case the old code got wrong: one blip meant a
// permanently badly-titled chat.
func TestGenerateTitleWorkflow_RetriesLLMBeforeFallback(t *testing.T) {
	env, calls := newTitleEnv(t, func(input map[string]interface{}, call int) (map[string]interface{}, error) {
		// Fail once, then succeed — a provider blip.
		if call == 1 {
			return nil, errors.New("error parsing response json: invalid character '\\x03'")
		}
		return map[string]interface{}{"title": "Debugging Title Generation"}, nil
	})

	env.ExecuteWorkflow(GenerateTitleWorkflow, titleInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.Len(t, *calls, 2, "a transient failure must be retried")
	for i, c := range *calls {
		assert.False(t, usedFallback(c), "call %d must ask the LLM, not the fallback", i)
	}
}

// Once the retries are spent, the workflow settles for the fallback rather
// than leaving the chat untitled forever — the activity short-circuits on any
// later run, so this is the last chance to write a title.
func TestGenerateTitleWorkflow_FallsBackAfterRetriesExhausted(t *testing.T) {
	env, calls := newTitleEnv(t, func(input map[string]interface{}, call int) (map[string]interface{}, error) {
		if usedFallback(input) {
			return map[string]interface{}{"title": "creating titles of chats is broken"}, nil
		}
		return nil, errors.New("provider down")
	})

	env.ExecuteWorkflow(GenerateTitleWorkflow, titleInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(),
		"a total LLM outage must still title the chat rather than fail the workflow")

	require.Len(t, *calls, titleLLMAttempts+1,
		"expected %d LLM attempts then exactly one fallback", titleLLMAttempts)

	// Every attempt but the last is a real LLM attempt; the last is the fallback.
	for i := 0; i < titleLLMAttempts; i++ {
		assert.False(t, usedFallback((*calls)[i]), "call %d must be an LLM attempt", i)
	}
	last := (*calls)[len(*calls)-1]
	assert.True(t, usedFallback(last), "final call must be the fallback")

	// The fallback must carry the original input through.
	assert.Equal(t, "chat-123", last["chat_id"])
	assert.Equal(t, "creating titles of chats is broken", last["first_message"])
}

// The happy path must not invoke the fallback at all.
func TestGenerateTitleWorkflow_SuccessNeverUsesFallback(t *testing.T) {
	env, calls := newTitleEnv(t, func(input map[string]interface{}, call int) (map[string]interface{}, error) {
		return map[string]interface{}{"title": "Debugging Title Generation"}, nil
	})

	env.ExecuteWorkflow(GenerateTitleWorkflow, titleInput())

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Len(t, *calls, 1)
	assert.False(t, usedFallback((*calls)[0]))
}
