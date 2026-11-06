package chat

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"testing"
)

type resetTemporalClient struct {
	*startRecorder
	baseline map[string]interface{}
}

func (c *resetTemporalClient) QueryWorkflowWithOptions(_ context.Context, req *client.QueryWorkflowWithOptionsRequest) (*client.QueryWorkflowWithOptionsResponse, error) {
	if req.QueryType == "get_thread_reset_inputs" {
		return fakeQueryResponse(req, false, encodedInputs{c.baseline}, nil)
	}
	return fakeQueryResponse(req, false, nil, errors.New("query unavailable"))
}

func TestThreadResetUsesThreadBaselineNotRootPreset(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Active())
	before := setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": "manual"})
	service.deps.Temporal = &resetTemporalClient{temporal, map[string]interface{}{"mode": "plan", "tools": []any{"thread-only"}}}
	req := updateParams(fx.chatID, paramsOf(t, map[string]any{"mode": nil}))
	req.Msg.ThreadId = &fx.rootThreadID
	_, err := service.UpdateWorkflowParams(ctx, req)
	require.NoError(t, err)
	require.Len(t, temporal.signals, 1)
	assert.Equal(t, map[string]interface{}{"mode": "plan", "__thread": fx.rootThreadID, "__reset_keys": []string{"mode"}}, temporal.signals[0].Arg)
	after, err := repo.GetChatSelection(ctx, fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, before.Version, after.Version)
}

func TestModeResetUsesPresetAndFreshSendRemovesStoredOverride(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Completed())
	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)
	_, err = repo.CreatePreset(ctx, &db.Preset{ID: uuid.NewString(), UserID: "test-user", ProjectID: &chat.ProjectID, Name: "Plan default", Slug: "plan-default", Tag: "agent", Params: map[string]interface{}{"mode": "plan"}})
	require.NoError(t, err)
	chat.SelectedPresets = map[string]string{"default": "plan-default"}
	require.NoError(t, repo.UpdateChat(ctx, chat))
	setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": "manual"})
	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, paramsOf(t, map[string]any{"mode": nil})))
	require.NoError(t, err)
	assert.Equal(t, "plan", temporal.lastStart(t).Inputs["mode"])
	stored, err := repo.GetChatSelection(ctx, fx.chatID)
	require.NoError(t, err)
	assert.NotContains(t, stored.Params, "mode")
	update, err := service.launcher().BuildStateUpdateForActiveWorkflow(ctx, "test-user", chat, "builtin://agent", nil, paramsOf(t, map[string]any{"mode": nil}))
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"mode": "plan"}, update)
}

func TestModeResetStoresDeletionAndSignalsOnlyConcreteMode(t *testing.T) {
	for _, status := range []db.WorkflowStatus{db.Active(), db.Paused(), db.Completed(), db.Failed()} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx, fx, repo, temporal, service := selectionFixture(t, status)
			setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": "plan", "model": map[string]any{"id": sonnetOnClaude}, "skills": []any{"test"}})
			response, err := service.UpdateWorkflowParams(ctx, updateParams(fx.chatID, paramsOf(t, map[string]any{"mode": nil})))
			require.NoError(t, err)
			require.NotNil(t, response.Msg.Selection)
			stored := response.Msg.Selection.Params.AsMap()
			assert.NotContains(t, stored, "mode")
			assert.Equal(t, sonnetOnClaude, modelID(t, stored["model"]))
			assert.Equal(t, []any{"test"}, stored["skills"])
			if status == db.Active() || status == db.Paused() {
				require.Len(t, temporal.signals, 1)
				assert.Equal(t, map[string]interface{}{"mode": "auto"}, temporal.signals[0].Arg)
			} else {
				assert.Empty(t, temporal.signals)
			}
			chat, err := repo.GetChat(ctx, fx.chatID)
			require.NoError(t, err)
			next, err := service.launcher().BuildChatWorkflowInputs(ctx, "test-user", chat, "builtin://agent", chat.SelectedPresets, nil)
			require.NoError(t, err)
			assert.Equal(t, "auto", next["mode"])
		})
	}
}

func TestGroupResetFillsOnlyTouchedGroupAndPreservesItsModel(t *testing.T) {
	ctx, fx, repo, _, service := selectionFixture(t, db.Active())
	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)
	update, err := service.launcher().BuildStateUpdateForActiveWorkflow(ctx, "test-user", chat, "builtin://parallel-compete", nil, paramsOf(t, map[string]any{"implementer": map[string]any{"model": map[string]any{"id": sonnetOnClaude}}}))
	require.NoError(t, err)
	require.Len(t, update, 1)
	group := update["implementer"].(map[string]interface{})
	assert.Equal(t, sonnetOnClaude, modelID(t, group["model"]))
	assert.Equal(t, []interface{}{"tag:coding:default"}, group["tools"])
	assert.NotContains(t, update, "model")
	assert.NotContains(t, update, "mode")
}

func TestResumeModeReconciliationPreservesExecutionOwnedInputs(t *testing.T) {
	ctx, fx, repo, _, service := selectionFixture(t, db.Paused())
	setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": nil})
	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)
	update := map[string]interface{}{"mode": "plan", "model": "runtime-model", "tools": []string{"runtime-tool"}, "internal": "kept"}
	require.NoError(t, service.reconcileResumeMode(ctx, "test-user", chat, "builtin://agent", update, nil))
	assert.Equal(t, map[string]interface{}{"mode": "auto", "model": "runtime-model", "tools": []string{"runtime-tool"}, "internal": "kept"}, update)
	update["mode"] = "manual"
	require.NoError(t, service.reconcileResumeMode(ctx, "test-user", chat, "builtin://agent", update, map[string]interface{}{"mode": "manual"}))
	assert.Equal(t, "manual", update["mode"])
}

func TestResumeAfterModeDeletionSignalsDefaultBeforeWake(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			status, temporalStatus := db.Paused(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING
			if failed {
				status, temporalStatus = db.Failed(), enums.WORKFLOW_EXECUTION_STATUS_TERMINATED
			}
			ctx, fx, repo, temporal, service := carryFixture(t, status, temporalStatus, sonnetOnClaude)
			temporal.runInputs["mode"] = "plan"
			setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": nil})
			_, err := service.SendMessage(ctx, sendContinue(fx.chatID, nil))
			require.NoError(t, err)
			updated := false
			for _, signal := range temporal.signals {
				if signal.Name == workflow.SignalUpdateWorkflowState {
					values := signal.Arg.(map[string]interface{})
					if mode, ok := values["mode"]; ok {
						assert.Equal(t, "auto", mode)
						updated = true
					}
				}
				if signal.Name == workflow.SignalResume {
					require.True(t, updated, "default must arrive before resume")
				}
			}
			require.True(t, updated)
		})
	}
}

func TestThreadConcreteGroupRemainsCompatibleWithOldWorker(t *testing.T) {
	ctx, fx, repo, _, service := selectionFixture(t, db.Active())
	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)
	params := paramsOf(t, map[string]any{"implementer": map[string]any{"model": map[string]any{"id": sonnetOnClaude}}})
	resolved, err := service.resolveThreadResetParams(ctx, "test-user", chat, "builtin://parallel-compete", fx.rootThreadID, fx.rootThreadID, params)
	require.NoError(t, err)
	assert.Equal(t, params, resolved)
}

func TestThreadResetWithoutBaselineFailsWithoutMutation(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Active())
	before := setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": "plan"})
	req := updateParams(fx.chatID, paramsOf(t, map[string]any{"mode": nil}))
	req.Msg.ThreadId = &fx.rootThreadID
	_, err := service.UpdateWorkflowParams(ctx, req)
	require.Error(t, err)
	assert.Empty(t, temporal.signals)
	after, err := repo.GetChatSelection(ctx, fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, before.Version, after.Version)
	assert.Equal(t, "plan", after.Params["mode"])
}
