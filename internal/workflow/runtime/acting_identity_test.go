// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func TestActingIdentity_ReadsTheRecordedIdentity(t *testing.T) {
	assert.Equal(t, "app", ActingIdentity(map[string]interface{}{InputKeyActingIdentity: "app"}))
	assert.Equal(t, "", ActingIdentity(map[string]interface{}{}))
	assert.Equal(t, "", ActingIdentity(nil))
	assert.Equal(t, "", ActingIdentity(map[string]interface{}{InputKeyActingIdentity: 7}), "a non-string is no identity, never a truthy one")
}

// Children inherit the identity, monotonically, like unattended: a sub-workflow,
// loop body or spawned agent cannot opt out of what its parent was given.
func TestPropagateActingIdentity(t *testing.T) {
	parent := map[string]interface{}{InputKeyActingIdentity: "app", InputKeyAutomation: "pr-review"}
	child := map[string]interface{}{"x": 1}
	propagateActingIdentity(parent, child)
	assert.Equal(t, "app", child[InputKeyActingIdentity])
	assert.Equal(t, "pr-review", child[InputKeyAutomation])
	assert.Equal(t, 1, child["x"])

	t.Run("a child that tried to set another identity is overwritten", func(t *testing.T) {
		c := map[string]interface{}{InputKeyActingIdentity: "owner"}
		propagateActingIdentity(parent, c)
		assert.Equal(t, "app", c[InputKeyActingIdentity])
	})
	t.Run("an owner parent adds nothing", func(t *testing.T) {
		c := map[string]interface{}{}
		propagateActingIdentity(map[string]interface{}{}, c)
		assert.Empty(t, c)
	})
	t.Run("an owner run's child cannot be handed the bot through its args", func(t *testing.T) {
		c := map[string]interface{}{InputKeyActingIdentity: "app", InputKeyAutomation: "forged", "x": 1}
		propagateActingIdentity(map[string]interface{}{}, c)
		assert.NotContains(t, c, InputKeyActingIdentity)
		assert.NotContains(t, c, InputKeyAutomation)
		assert.Equal(t, 1, c["x"])
	})
	t.Run("nil child is a no-op", func(t *testing.T) {
		assert.NotPanics(t, func() { propagateActingIdentity(parent, nil) })
	})
}

// A run a person starts never acts as the bot: a reply in the chat, a resumed
// run, a late wake. DropActingIdentity is the one place that rule is written.
func TestDropActingIdentity(t *testing.T) {
	inputs := map[string]interface{}{InputKeyActingIdentity: "app", InputKeyAutomation: "pr-review", InputKeyLaunchRun: true, "keep": "me"}
	DropActingIdentity(inputs)
	assert.NotContains(t, inputs, InputKeyActingIdentity)
	assert.NotContains(t, inputs, InputKeyAutomation)
	assert.Equal(t, "me", inputs["keep"])
	assert.NotPanics(t, func() { DropActingIdentity(nil) })
}

// The late-wake successor is a run a person's reply started; it must not carry
// the bot's identity across, exactly as it does not carry the launch marker.
func TestLateWakeSuccessorDropsTheBotIdentity(t *testing.T) {
	in := WorkflowInput{ChatID: "c", Inputs: map[string]interface{}{
		InputKeyActingIdentity: "app", InputKeyAutomation: "pr-review", InputKeyLaunchRun: true, InputKeyUnattended: true,
	}}
	out := lateWakeSuccessorInput(in)
	assert.NotContains(t, out.Inputs, InputKeyActingIdentity)
	assert.NotContains(t, out.Inputs, InputKeyAutomation)
	assert.NotContains(t, out.Inputs, InputKeyLaunchRun)
	assert.Contains(t, in.Inputs, InputKeyActingIdentity, "the original input map is not mutated")
}

// The engine stamps every activity's runtime context with the run's identity,
// read from the run's own inputs, so an `action` node and an agent's tools both
// see it. A node type is no exception: the identity is the run's, not the node's.
func TestBuildRuntimeContextCarriesTheRunsActingIdentity(t *testing.T) {
	inputs := map[string]interface{}{InputKeyActingIdentity: "app", InputKeyAutomation: "pr-review"}
	// call_llm is left out only because its context needs a Temporal workflow
	// (GetVersion); the identity is stamped before any node-type branch.
	for _, nodeType := range []string{"action", "execute_tools", "run"} {
		e := &StepExecutor{workflowID: "w", chatID: "c", workflowInputs: inputs}
		rtx := e.buildRuntimeContext(&reliantv1.Node{Id: "n", Type: nodeType})
		assert.Equal(t, "app", rtx.ActingIdentity, nodeType)
		assert.Equal(t, "pr-review", rtx.Automation, nodeType)
	}

	owner := (&StepExecutor{workflowID: "w", chatID: "c", workflowInputs: map[string]interface{}{}}).
		buildRuntimeContext(&reliantv1.Node{Id: "n", Type: "action"})
	assert.Empty(t, owner.ActingIdentity)
	assert.Empty(t, owner.Automation)

	assert.NotPanics(t, func() {
		(&StepExecutor{workflowID: "w", chatID: "c"}).buildRuntimeContext(&reliantv1.Node{Id: "n", Type: "action"})
	}, "a nil input map is an owner run")
}
