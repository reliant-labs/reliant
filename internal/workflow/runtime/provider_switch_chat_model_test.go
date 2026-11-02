// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The prod shape (2026-10-31..11-02): a chat bound to claude spawns a project
// preset that pins its model to another provider (code_reviewer: gpt-6.1-sol
// on codex). The spawned thread's model call must carry the chat's own model,
// which it runs on instead of holding, and must not claim the user chose its
// model.
func TestProviderSwitchSpawnOfOffProviderPresetCarriesTheChatModel(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)
	input := spawnE2EWorkflowInput("provider-switch-chat-model")
	input.Inputs["model"] = map[string]interface{}{"id": "claude-5.5-opus@claude"}
	input.Inputs[InputKeyProviderSwitch] = "claude"
	e := newSubAgentFailureEnv(t, env, input.ExecContext.Thread)
	env.OnActivity("LoadPresetParams", mock.Anything, mock.Anything).Return(map[string]interface{}{
		"model": map[string]interface{}{"id": "gpt-6.1-sol", "providers": []interface{}{"codex"}},
	}, nil)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("reviewer", "review the PR")}, nil
		}
		return nil, nil
	}
	e.childTurn = func(string, int) (map[string]interface{}, error) {
		return map[string]interface{}{"response_text": "reviewed"}, nil
	}
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.NoError(t, env.GetWorkflowError())

	e.mu.Lock()
	defer e.mu.Unlock()
	rtx, ok := e.callRuntime["reviewer#1"]
	require.True(t, ok, "the spawned reviewer made a model call")
	require.Equal(t, "claude", rtx.ChatProvider)
	require.True(t, rtx.Descendant)
	require.Equal(t, &types.ModelChoice{ID: "claude-5.5-opus@claude"}, rtx.ChatModel,
		"the descendant carries the chat's model to fall back to")
	require.False(t, rtx.ThreadModelChosen, "a preset's model is a default, not the user's choice")

	root, ok := e.callRuntime["parent#1"]
	require.True(t, ok)
	require.False(t, root.Descendant)
	require.Nil(t, root.ChatModel, "the root's own model IS the chat's model; it has nothing to fall back to")
}

func TestChildWorkflowTrackerChatModelFollowsTheRootsInputs(t *testing.T) {
	t.Parallel()
	inputs := map[string]interface{}{"model": map[string]interface{}{"id": "claude-5.5-opus@claude"}}
	tracker := &ChildWorkflowTracker{rootInputs: inputs}
	require.Equal(t, &types.ModelChoice{ID: "claude-5.5-opus@claude"}, tracker.chatModel())

	// A global update (the chat's picker) writes the root's map in place.
	inputs["model"] = map[string]interface{}{"tags": []interface{}{"flagship"}, "providers": []interface{}{"claude"}}
	require.Equal(t, &types.ModelChoice{Tags: []string{"flagship"}, Providers: []string{"claude"}}, tracker.chatModel())

	inputs["model"] = "gpt-6.1-sol@codex"
	require.Equal(t, &types.ModelChoice{ID: "gpt-6.1-sol@codex"}, tracker.chatModel())

	delete(inputs, "model")
	require.Nil(t, tracker.chatModel())
	require.Nil(t, (*ChildWorkflowTracker)(nil).chatModel())
}

func TestChildWorkflowTrackerThreadChoseModelOnlyForAUsersThreadEdit(t *testing.T) {
	t.Parallel()
	tracker := &ChildWorkflowTracker{}
	require.False(t, tracker.threadChoseModel("reviewer"))

	tracker.recordThreadInputOverride("reviewer", map[string]interface{}{"thinking_level": "low"})
	require.False(t, tracker.threadChoseModel("reviewer"), "editing another param is not a model choice")

	tracker.recordThreadInputOverride("reviewer", map[string]interface{}{"model": map[string]interface{}{"id": "gpt-6.1-sol"}})
	require.True(t, tracker.threadChoseModel("reviewer"))
	require.False(t, tracker.threadChoseModel("other"))
}
