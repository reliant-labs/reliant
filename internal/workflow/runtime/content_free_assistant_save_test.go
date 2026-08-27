// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// An assistant message with nothing in it cannot be written: SaveMessage's
// validator refuses the row, because a blockless assistant row is durable
// poison for the thread. Dispatching anyway buys five failing attempts, an
// ERROR per attempt, and an error banner the user cannot act on.
//
// These tests run the real executeSaveMessageInline against a Temporal test
// environment with a recording SaveMessage stub, so the assertion is on the
// actual dispatch — not on a re-implementation of the predicate.

func celLit(s string) *reliantv1.CelString {
	return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: s}}
}

// runInlineSave executes one inline save_message and reports whether the
// SaveMessage activity was dispatched.
func runInlineSave(
	t *testing.T,
	cfg *reliantv1.SaveMessageConfig,
	activityOutput map[string]interface{},
) (dispatched bool, err error) {
	t.Helper()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	calls := 0
	env.RegisterActivityWithOptions(
		func(context.Context, json.RawMessage) (map[string]interface{}, error) {
			calls++
			return map[string]interface{}{
				"message_id":         "msg-1",
				"thread_token_count": 0,
			}, nil
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)

	workflowContext := map[string]interface{}{
		workflowContextKeyID:     "wf-1",
		workflowContextKeyName:   "test-workflow",
		workflowContextKeyChatID: "chat-1",
		workflowContextKeyInputs: map[string]interface{}{"thread": "thread-1"},
	}

	env.ExecuteWorkflow(func(ctx workflow.Context) (map[string]interface{}, error) {
		node := &reliantv1.Node{Id: "step-1", SaveMessage: cfg}
		return executeSaveMessageInline(
			ctx,
			node,
			activityOutput,
			workflowContext,
			map[string]interface{}{},
			"chat-1",
			"wf-1",
			"", 0,
			&ExecutionContext{WorkflowID: "wf-1", ChatID: "chat-1", Thread: "thread-1"},
			"",
		)
	})

	require.True(t, env.IsWorkflowCompleted())
	return calls > 0, env.GetWorkflowError()
}

// The skip itself: role=assistant with no content, no tool calls and no
// thinking must return cleanly without dispatching anything.
func TestInlineSave_ContentFreeAssistantIsSkipped(t *testing.T) {
	dispatched, err := runInlineSave(t,
		&reliantv1.SaveMessageConfig{Role: celLit("assistant")},
		map[string]interface{}{},
	)

	require.NoError(t, err, "declining the dispatch is a clean skip, not a failure")
	require.False(t, dispatched,
		"a content-free assistant message has no row to write; dispatching buys "+
			"five failing attempts and an error banner the user cannot act on")
}

// The skip is keyed on the role, and Role is matched case-insensitively
// (strings.EqualFold), so the spelling must not decide whether poison is
// written.
func TestInlineSave_ContentFreeAssistantSkipIsCaseInsensitive(t *testing.T) {
	for _, role := range []string{"Assistant", "ASSISTANT"} {
		t.Run(role, func(t *testing.T) {
			dispatched, err := runInlineSave(t,
				&reliantv1.SaveMessageConfig{Role: celLit(role)},
				map[string]interface{}{},
			)
			require.NoError(t, err)
			require.False(t, dispatched)
		})
	}
}

// The other side of the gate. Each of the three carriers of content, alone, is
// enough to make the message real — the skip must not swallow a turn that had
// something to say.
func TestInlineSave_AnyContentStillDispatches(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		dispatched, err := runInlineSave(t,
			&reliantv1.SaveMessageConfig{
				Role:    celLit("assistant"),
				Content: celLit("hello"),
			},
			map[string]interface{}{},
		)
		require.NoError(t, err)
		require.True(t, dispatched, "an assistant message with text must be saved")
	})

	t.Run("tool_calls_only", func(t *testing.T) {
		dispatched, err := runInlineSave(t,
			&reliantv1.SaveMessageConfig{
				Role:      celLit("assistant"),
				ToolCalls: celLit("{{output.tool_calls}}"),
			},
			map[string]interface{}{
				"tool_calls": []interface{}{
					map[string]interface{}{
						"id":    "toolu_1",
						"name":  "bash",
						"input": `{"command":"ls"}`,
					},
				},
			},
		)
		require.NoError(t, err)
		require.True(t, dispatched,
			"a tool-calling turn carries no text but is the whole point of the turn")
	})

	// The one that matters most. SaveMessage counts a thinking block as
	// content, so a reasoning-only turn is NOT blockless — see
	// activities/handlers/blockless_assistant_test.go, which documents why
	// dropping it corrupts multi-turn thinking. If the skip ever widened to
	// cover thinking, that turn would vanish silently.
	t.Run("thinking_only", func(t *testing.T) {
		dispatched, err := runInlineSave(t,
			&reliantv1.SaveMessageConfig{Role: celLit("assistant")},
			map[string]interface{}{
				"thinking": map[string]interface{}{
					"content":   "let me reason about this",
					"signature": "sig-abc",
				},
			},
		)
		require.NoError(t, err)
		require.True(t, dispatched,
			"a reasoning-only assistant message carries a thinking block and must be kept")
	})
}

// The skip is scoped to the assistant role. A content-free USER or TOOL
// message is a different question with a different validator, and must not be
// swept up by this gate.
func TestInlineSave_NonAssistantRolesAreUnaffected(t *testing.T) {
	for _, role := range []string{"user", "tool", "system"} {
		t.Run(role, func(t *testing.T) {
			dispatched, err := runInlineSave(t,
				&reliantv1.SaveMessageConfig{Role: celLit(role)},
				map[string]interface{}{},
			)
			require.NoError(t, err)
			require.True(t, dispatched,
				"the content-free skip is an assistant-role rule and must not widen to %s", role)
		})
	}
}
