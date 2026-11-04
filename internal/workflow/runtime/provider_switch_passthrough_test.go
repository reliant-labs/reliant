// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/core"
)

// A fork or inline node that passes `model` through is meant to track the
// root's model. After a chat-wide provider switch the global update must reach
// it, while a thread that OWNS its model (a preset or node arg) keeps it.
// Inputs are assembled by the real buildSubWorkflowInputsWithOwnership, and
// the update goes through the real setupInputUpdateHandler.

type passthroughResult struct {
	ForkModel      interface{}
	OwnedModel     interface{}
	ChatProvider   string
	RootModel      interface{}
	ForkIsOwned    bool
	ForkHasPreset  bool
	OwnedHasPreset bool
}

func passthroughWorkflow(ctx workflow.Context, boundTo string) (passthroughResult, error) {
	oldModel := map[string]interface{}{"id": "claude-5.5-opus@claude"}
	rootInputs := map[string]interface{}{"model": oldModel}

	tracker := &ChildWorkflowTracker{children: map[string]bool{}, chatProvider: boundTo, rootThread: "root"}

	executor := &InlineWorkflowExecutor{
		ctx:            ctx,
		workflowInputs: rootInputs,
		logger:         &runtimeBridgeNoopLogger{},
		node: &reliantv1.Node{
			Id: "launch_preview", Type: "workflow",
			Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Passthrough: []string{"model"}}},
		},
		invocationContract: &core.SubWorkflowContract{InputPolicy: core.InputPolicyRefPresetsArgsDefaults},
	}
	executor.evalResult = executor.node
	forkInputs, forkOwned, err := executor.buildSubWorkflowInputsWithOwnership()
	if err != nil {
		return passthroughResult{}, err
	}
	tracker.RegisterThreadInputs("fork", forkInputs, forkOwned)

	// A thread whose preset owns a semantic selector: the contrast.
	ownedInputs := map[string]interface{}{
		"model": map[string]interface{}{"tags": []interface{}{"fast"}},
	}
	tracker.RegisterThreadInputs("owned", ownedInputs, map[string]bool{"model": true})

	setupInputUpdateHandler(ctx, rootInputs, tracker, "wf", workflow.NewChannel(ctx))

	// The switch is signalled from the delayed callback; wait for it to land.
	if err := workflow.Sleep(ctx, 2*time.Minute); err != nil {
		return passthroughResult{}, err
	}
	_, forkHasPreset := forkInputs[inputKeyPresetModel]
	_, ownedHasPreset := ownedInputs[inputKeyPresetModel]
	return passthroughResult{
		ForkModel: forkInputs["model"], OwnedModel: ownedInputs["model"],
		ChatProvider: tracker.chatProvider, RootModel: rootInputs["model"],
		ForkIsOwned: forkOwned["model"], ForkHasPreset: forkHasPreset, OwnedHasPreset: ownedHasPreset,
	}, nil
}

func runPassthrough(t *testing.T, boundTo string, update map[string]interface{}) passthroughResult {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)
	env.RegisterWorkflow(passthroughWorkflow)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("update_workflow_state", update)
	}, time.Minute)
	env.ExecuteWorkflow(passthroughWorkflow, boundTo)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got passthroughResult
	require.NoError(t, env.GetWorkflowResult(&got))
	return got
}

func TestPassthroughForkFollowsGlobalModelUpdateAfterProviderSwitch(t *testing.T) {
	t.Parallel()
	switched := map[string]interface{}{"id": "gpt-6.1-sol@codex"}
	got := runPassthrough(t, "claude", map[string]interface{}{"model": switched, InputKeyProviderSwitch: "codex"})

	assert.Equal(t, "codex", got.ChatProvider, "the update is a chat-wide provider switch")
	assert.Equal(t, switched, got.RootModel)
	assert.False(t, got.ForkIsOwned, "a passthrough key is not owned by the thread: it exists to track the parent")
	assert.Equal(t, switched, got.ForkModel,
		"a passthrough fork follows the root's switched model instead of keeping its old exact model (and holding on the new provider)")
	assert.Equal(t, map[string]interface{}{"tags": []interface{}{"fast"}}, got.OwnedModel,
		"a thread that owns its model selector keeps it across the switch")
}

// The model alone binds nothing, even one that names a provider: only the
// server's explicit switch does. The fork still follows the root's model.
func TestPassthroughForkFollowsAnOrdinaryModelUpdateWithoutBinding(t *testing.T) {
	t.Parallel()
	picked := map[string]interface{}{"id": "gpt-6.1-sol@codex"}
	got := runPassthrough(t, "", map[string]interface{}{"model": picked})

	assert.Empty(t, got.ChatProvider, "an ordinary model pick is not a provider switch")
	assert.Equal(t, picked, got.ForkModel, "a passthrough fork follows the root's model")
	assert.Equal(t, map[string]interface{}{"tags": []interface{}{"fast"}}, got.OwnedModel)
}
