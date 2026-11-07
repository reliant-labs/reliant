package runtime

import (
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/workflow"
)

func TestThreadResetBaselineLifecycle(t *testing.T) {
	tracker := &ChildWorkflowTracker{}
	inputs := map[string]interface{}{"model": "preset-model", "mode": "auto", "temperature": 0.2}
	owned := map[string]bool{"model": true}
	overlayNodeArgs(inputs, owned, map[string]interface{}{"temperature": 0.4})
	tracker.captureThreadResetInputs("child", inputs, owned)
	tracker.recordThreadInputOverride("child", map[string]interface{}{"model": "user-model", "mode": "plan"})
	tracker.applyThreadInputOverrides("child", inputs, owned)
	tracker.RegisterThreadInputs("child", inputs, owned)
	baseline, err := tracker.getThreadResetInputs("child")
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"model": "preset-model", "mode": "auto", "temperature": 0.4}, baseline)
	baseline["model"] = "query-mutation"
	tracker.updateThreadResetInputs(map[string]interface{}{"model": "root-model", "mode": "chat", "temperature": 0.9})
	baseline, err = tracker.getThreadResetInputs("child")
	require.NoError(t, err)
	require.Equal(t, "preset-model", baseline["model"])
	require.Equal(t, "chat", baseline["mode"])
	require.Equal(t, 0.4, baseline["temperature"])
	require.Equal(t, "plan", inputs["mode"])
	tracker.UnregisterThreadInputs("child")
	tracker.updateThreadResetInputs(map[string]interface{}{"mode": "auto"})
	baseline, err = tracker.getThreadResetInputs("child")
	require.NoError(t, err)
	require.Equal(t, "auto", baseline["mode"])
	// Reconstruction refreshes baseline, then reapplies the user's choice.
	rebuilt := map[string]interface{}{"model": "new-preset", "mode": "chat"}
	rebuiltOwned := map[string]bool{"model": true}
	tracker.captureThreadResetInputs("child", rebuilt, rebuiltOwned)
	tracker.applyThreadInputOverrides("child", rebuilt, rebuiltOwned)
	require.Equal(t, "user-model", rebuilt["model"])
	baseline, err = tracker.getThreadResetInputs("child")
	require.NoError(t, err)
	require.Equal(t, "new-preset", baseline["model"])
	_, err = tracker.getThreadResetInputs("missing")
	require.ErrorContains(t, err, "reset inputs unavailable")
}

func TestThreadResetSignalReleasesOnlyUserOwnership(t *testing.T) {
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{"__thread": "child", "model": "preset", "mode": "auto", "__reset_keys": []string{"model", "mode"}})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{"model": "root-new", "mode": "chat"})
	}, 2*time.Second)
	env.ExecuteWorkflow(func(ctx workflow.Context) (map[string]interface{}, error) {
		tracker := &ChildWorkflowTracker{}
		inputs := map[string]interface{}{"model": "preset", "mode": "auto"}
		owned := map[string]bool{"model": true}
		tracker.captureThreadResetInputs("child", inputs, owned)
		tracker.recordThreadInputOverride("child", map[string]interface{}{"model": "user", "mode": "plan"})
		tracker.applyThreadInputOverrides("child", inputs, owned)
		tracker.RegisterThreadInputs("child", inputs, owned)
		setupInputUpdateHandler(ctx, map[string]interface{}{}, tracker, "root", workflow.NewChannel(ctx))
		if err := workflow.Sleep(ctx, 3*time.Second); err != nil {
			return nil, err
		}
		require.Empty(t, tracker.threadInputOverrides["child"])
		require.True(t, tracker.threadOwnsKey("child", "model"))
		require.False(t, tracker.threadOwnsKey("child", "mode"))
		require.NotContains(t, inputs, "__reset_keys")
		return inputs, nil
	})
	require.NoError(t, env.GetWorkflowError())
	var inputs map[string]interface{}
	require.NoError(t, env.GetWorkflowResult(&inputs))
	require.Equal(t, "preset", inputs["model"])
	require.Equal(t, "chat", inputs["mode"])
}

func TestThreadResetNestedInputsAreIndependent(t *testing.T) {
	tracker := &ChildWorkflowTracker{}
	group := map[string]interface{}{"tags": []interface{}{"preset"}}
	tracker.captureThreadResetInputs("child", map[string]interface{}{"model": group}, nil)
	group["tags"].([]interface{})[0] = "live"
	baseline, err := tracker.getThreadResetInputs("child")
	require.NoError(t, err)
	require.Equal(t, "preset", baseline["model"].(map[string]interface{})["tags"].([]interface{})[0])
	baseline["model"].(map[string]interface{})["tags"] = "query mutation"
	tracker.updateThreadResetInputs(map[string]interface{}{"model": group, "__provider_switch": "internal", "unknown": "not an input"})
	group["tags"] = "later mutation"
	baseline, err = tracker.getThreadResetInputs("child")
	require.NoError(t, err)
	require.Equal(t, []interface{}{"live"}, baseline["model"].(map[string]interface{})["tags"])
	require.NotContains(t, baseline, "__provider_switch")
	require.NotContains(t, baseline, "unknown")
}

func TestThreadResetUsesLatestBaselineAfterQuery(t *testing.T) {
	tracker := &ChildWorkflowTracker{}
	inputs := map[string]interface{}{"mode": "auto", "temperature": 0.2}
	owned := map[string]bool{}
	tracker.captureThreadResetInputs("child", inputs, owned)
	tracker.recordThreadInputOverride("child", map[string]interface{}{"mode": "plan"})
	tracker.applyThreadInputOverrides("child", inputs, owned)
	tracker.RegisterThreadInputs("child", inputs, owned)

	// Backend queried auto, but a root update arrives before its reset signal.
	snapshot, err := tracker.getThreadResetInputs("child")
	require.NoError(t, err)
	tracker.updateThreadResetInputs(map[string]interface{}{"mode": "manual", "temperature": 0.9})
	update := map[string]interface{}{"mode": snapshot["mode"], "temperature": 0.5, "__reset_keys": []string{"mode"}}
	overrides := tracker.consumeThreadResetKeys("child", update)
	tracker.recordThreadInputOverride("child", overrides)
	for key, value := range update {
		inputs[key] = value
	}
	require.Equal(t, "manual", inputs["mode"])
	require.False(t, tracker.threadOwnsKey("child", "mode"))
	require.NotContains(t, tracker.threadInputOverrides["child"], "mode")
	// Unmarked edits retain their supplied value and override semantics.
	require.Equal(t, 0.5, inputs["temperature"])
	require.Equal(t, 0.5, tracker.threadInputOverrides["child"]["temperature"])
	require.NotContains(t, inputs, "__reset_keys")
}

func TestThreadResetWithoutBaselineKeepsConcreteValue(t *testing.T) {
	tracker := &ChildWorkflowTracker{}
	update := map[string]interface{}{"mode": "auto", "__reset_keys": []string{"mode"}}
	require.Empty(t, tracker.consumeThreadResetKeys("child", update))
	require.Equal(t, "auto", update["mode"])
}

func TestThreadResetWhileStoppedDoesNotPersistOverride(t *testing.T) {
	tracker := &ChildWorkflowTracker{lastThreadInputs: map[string]map[string]interface{}{"child": {"mode": "plan"}}}
	tracker.captureThreadResetInputs("child", map[string]interface{}{"mode": "auto"}, nil)
	tracker.recordThreadInputOverride("child", map[string]interface{}{"mode": "plan"})
	update := map[string]interface{}{"mode": "auto", "__reset_keys": []interface{}{"mode"}}
	tracker.recordThreadInputOverride("child", tracker.consumeThreadResetKeys("child", update))
	require.Empty(t, tracker.threadInputOverrides["child"])
	require.Equal(t, "auto", tracker.lastThreadInputs["child"]["mode"])
	rebuilt := map[string]interface{}{"mode": "chat"}
	tracker.applyThreadInputOverrides("child", rebuilt, map[string]bool{})
	require.Equal(t, "chat", rebuilt["mode"])
	require.NotContains(t, update, "__reset_keys")
}
