// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

func policyOf(onLimit, fallback string) map[string]interface{} {
	return types.RunProviderPolicy{OnLimit: onLimit, FallbackModel: fallback}.Value()
}

func TestProviderPolicyIsRuntimeInjected(t *testing.T) {
	t.Parallel()
	assert.True(t, workflow.RuntimeInjectedInputs[InputKeyProviderPolicy],
		"a client must not be able to set the run's provider policy")
	assert.Equal(t, preset.InputKeyProviderPolicy, InputKeyProviderPolicy)
	assert.Equal(t, preset.InputKeyProviderPreference, InputKeyProviderPreference)
}

func TestProviderPolicy(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		inputs map[string]interface{}
		want   types.RunProviderPolicy
	}{
		"nil inputs wait": {nil, types.RunProviderPolicy{}},
		"no policy waits": {map[string]interface{}{"model": "m@claude"}, types.RunProviderPolicy{}},
		"a fallback to another provider stands": {
			map[string]interface{}{"model": "claude-5.5-opus@claude", InputKeyProviderPolicy: policyOf("fallback", "gpt-5.6-sol@codex")},
			types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: "gpt-5.6-sol@codex"},
		},
		"a fallback onto the primary provider is ignored": {
			map[string]interface{}{"model": "claude-5.5-opus@claude", InputKeyProviderPolicy: policyOf("fallback", "claude-5.5-sonnet@claude")},
			types.RunProviderPolicy{OnLimit: "wait"},
		},
		"the primary is the inherited preference when the model names none": {
			map[string]interface{}{"model": map[string]interface{}{"tags": []interface{}{"fast"}}, InputKeyProviderPreference: []interface{}{"codex"}, InputKeyProviderPolicy: policyOf("fallback", "gpt-5.6-sol@codex")},
			types.RunProviderPolicy{OnLimit: "wait"},
		},
		"no known primary leaves the fallback": {
			map[string]interface{}{InputKeyProviderPolicy: policyOf("fallback", "gpt-5.6-sol@codex")},
			types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: "gpt-5.6-sol@codex"},
		},
		"survives the json round trip a stored input takes": {
			jsonRoundTrip(t, map[string]interface{}{"model": "claude-5.5-opus@claude", InputKeyProviderPolicy: policyOf("fallback", "gpt-5.6-sol@codex")}),
			types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: "gpt-5.6-sol@codex"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ProviderPolicy(tc.inputs))
		})
	}
}

func jsonRoundTrip(t *testing.T, in map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, err := json.Marshal(in)
	require.NoError(t, err)
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

func TestPropagateProviderPreference_CarriesThePolicy(t *testing.T) {
	t.Parallel()
	parent := map[string]interface{}{"model": "claude-5.5-opus@claude", InputKeyProviderPolicy: policyOf("fallback", "gpt-5.6-sol@codex")}

	t.Run("inherited when the child set none", func(t *testing.T) {
		child := map[string]interface{}{}
		propagateProviderPreference(parent, child)
		assert.Equal(t, types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: "gpt-5.6-sol@codex"}, readProviderPolicy(child))
	})
	t.Run("the child's own on_limit wins over the inherited one", func(t *testing.T) {
		child := map[string]interface{}{InputKeyProviderPolicy: policyOf("wait", "")}
		propagateProviderPreference(parent, child)
		assert.Equal(t, types.RunProviderPolicy{OnLimit: "wait"}, readProviderPolicy(child))
	})
	t.Run("the child's accounts merge over the inherited ones per driver", func(t *testing.T) {
		p := map[string]interface{}{InputKeyProviderPolicy: types.RunProviderPolicy{OnLimit: "wait", PreferredAccount: map[string]string{"claude": "a@x", "codex": "c@x"}}.Value()}
		child := map[string]interface{}{InputKeyProviderPolicy: types.RunProviderPolicy{PreferredAccount: map[string]string{"claude": "b@x"}}.Value()}
		propagateProviderPreference(p, child)
		got := readProviderPolicy(child)
		assert.Equal(t, "wait", got.OnLimit)
		assert.Equal(t, map[string]string{"claude": "b@x", "codex": "c@x"}, got.PreferredAccount)
	})
	t.Run("a parent with no policy adds none", func(t *testing.T) {
		child := map[string]interface{}{}
		propagateProviderPreference(map[string]interface{}{"model": "m@claude"}, child)
		assert.NotContains(t, child, InputKeyProviderPolicy)
	})
}

// One test per call site, mirroring T2's: every place a child's inputs are
// assembled hands the policy down.
func TestPolicyReachesEveryChildCallSite(t *testing.T) {
	t.Parallel()
	parent := func() map[string]interface{} {
		return map[string]interface{}{"model": "claude-5.5-opus@claude", "mode": "auto", InputKeyProviderPolicy: policyOf("fallback", "gpt-5.6-sol@codex")}
	}
	want := types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: "gpt-5.6-sol@codex"}

	t.Run("sub-workflow (ref)", func(t *testing.T) {
		e := &InlineWorkflowExecutor{
			workflowInputs:     parent(),
			subWorkflow:        &reliantv1.Workflow{},
			invocationContract: &core.SubWorkflowContract{InputPolicy: core.InputPolicyRefPresetsArgsDefaults},
		}
		assert.Equal(t, want, readProviderPolicy(e.buildSubWorkflowInputs()))
	})
	t.Run("sub-workflow (inline) shares the parent's map", func(t *testing.T) {
		e := &InlineWorkflowExecutor{
			workflowInputs:     parent(),
			invocationContract: &core.SubWorkflowContract{InputPolicy: core.InputPolicyInlineInheritParentInputs},
		}
		assert.Equal(t, want, readProviderPolicy(e.buildSubWorkflowInputs()))
	})
	loopNode := &reliantv1.Node{Id: "loop", Type: "loop", Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
		While: &reliantv1.DirectCelBool{Expr: "iter.iteration < 1"},
		Args:  map[string]*structpb.Value{"task": structpb.NewStringValue("t")},
	}}}
	newLoop := func() *InlineLoopExecutor {
		return &InlineLoopExecutor{
			loopID:             "loop",
			loopStep:           &core.TriggeredNode{Node: loopNode},
			workflowID:         "wf-1",
			workflowName:       "builtin://agent",
			workflowInputs:     parent(),
			nodeOutputs:        map[string]interface{}{},
			subWorkflow:        &reliantv1.Workflow{},
			invocationContract: &core.SubWorkflowContract{InputPolicy: core.InputPolicyRefPresetsArgsDefaults},
		}
	}
	t.Run("loop body", func(t *testing.T) {
		inputs, err := newLoop().buildIterationInputs()
		require.NoError(t, err)
		assert.Equal(t, want, readProviderPolicy(inputs))
	})
	t.Run("parallel loop branch", func(t *testing.T) {
		inputs, err := newLoop().buildParallelIterationInputs(nil, 0, "item", "k")
		require.NoError(t, err)
		assert.Equal(t, want, readProviderPolicy(inputs))
	})
	t.Run("spawn child", func(t *testing.T) {
		// buildSpawnChildInputs leaves it to the inline executor that runs the
		// spawn node (which sees the child's preset and args first).
		args := buildSpawnChildInputs(parent())
		node := newSpawnNode("tc1", spawnTargetWorkflow, "", args)
		e := &InlineWorkflowExecutor{
			workflowInputs:     parent(),
			node:               node,
			subWorkflow:        &reliantv1.Workflow{},
			subWorkflowInputs:  map[string]interface{}{},
			invocationContract: &core.SubWorkflowContract{InputPolicy: core.InputPolicyRefPresetsArgsDefaults},
		}
		for k, v := range node.GetWorkflow().GetArgs() {
			e.subWorkflowInputs[k] = v.AsInterface()
		}
		assert.Equal(t, want, readProviderPolicy(e.buildSubWorkflowInputs()))
	})
}

// A spawn call's `providers` beats its preset's, field by field, and both beat
// what the child would inherit.
func TestSpawnProvidersPrecedence(t *testing.T) {
	t.Parallel()
	parent := map[string]interface{}{"model": "claude-5.5-opus@claude", InputKeyProviderPolicy: policyOf("fallback", "gpt-5.6-sol@codex")}

	presetRouting := &preset.ProviderRouting{OnLimit: "wait", Prefer: []string{"claude:work@acme.com"}}
	spawnRouting, err := preset.ParseProviderRouting(map[string]interface{}{"on_limit": "fallback", "fallback": "gpt-5.6-sol@codex", "prefer": []interface{}{"codex"}})
	require.NoError(t, err)
	justSpawnPrefer, err := preset.ParseProviderRouting(map[string]interface{}{"prefer": []interface{}{"codex"}})
	require.NoError(t, err)

	// What the executor does: preset params merged first, spawn args over them,
	// inheritance last.
	build := func(p, spawn *preset.ProviderRouting) map[string]interface{} {
		args := buildSpawnChildInputs(parent)
		for k, v := range spawn.Inputs() {
			args[k] = v
		}
		node := newSpawnNode("tc1", spawnTargetWorkflow, "", args)
		nodeArgs := map[string]interface{}{}
		for k, v := range node.GetWorkflow().GetArgs() {
			nodeArgs[k] = v.AsInterface()
		}
		inputs := map[string]interface{}{}
		mergePresetParams(inputs, DefaultPresetGroup, (&preset.Preset{Providers: p}).InputParams())
		overlayNodeArgs(inputs, map[string]bool{}, nodeArgs)
		propagateProviderPreference(parent, inputs)
		return inputs
	}

	t.Run("inherited alone", func(t *testing.T) {
		got := build(nil, nil)
		assert.Equal(t, "fallback", readProviderPolicy(got).OnLimit)
	})
	t.Run("preset beats inherited", func(t *testing.T) {
		got := build(presetRouting, nil)
		assert.Equal(t, types.RunProviderPolicy{OnLimit: "wait", PreferredAccount: map[string]string{"claude": "work@acme.com"}}, readProviderPolicy(got))
		assert.Equal(t, []string{"claude"}, ProviderPreference(got))
	})
	t.Run("spawn beats preset", func(t *testing.T) {
		got := build(presetRouting, spawnRouting)
		assert.Equal(t, "fallback", readProviderPolicy(got).OnLimit)
		assert.Equal(t, []string{"codex"}, ProviderPreference(got))
		assert.Equal(t, "work@acme.com", readProviderPolicy(got).PreferredAccount["claude"], "an account the spawn did not name stays the preset's")
	})
	t.Run("a spawn that only prefers keeps the preset's on_limit", func(t *testing.T) {
		got := build(presetRouting, justSpawnPrefer)
		assert.Equal(t, "wait", readProviderPolicy(got).OnLimit)
		assert.Equal(t, []string{"codex"}, ProviderPreference(got))
	})
}

func TestParseSpawnToolInput_Providers(t *testing.T) {
	t.Parallel()
	got, err := parseSpawnToolInput(`{"preset":"coder","prompt":"go","providers":{"prefer":["codex"],"on_limit":"wait"}}`)
	require.NoError(t, err)
	require.NotNil(t, got.providers)
	assert.Equal(t, []string{"codex"}, got.providers.Prefer)

	got, err = parseSpawnToolInput(`{"preset":"coder","prompt":"go"}`)
	require.NoError(t, err)
	assert.Nil(t, got.providers)

	for name, in := range map[string]string{
		"unknown key":      `{"prompt":"go","providers":{"prefr":["codex"]}}`,
		"unknown provider": `{"prompt":"go","providers":{"prefer":["chatgpt"]}}`,
		"bad fallback":     `{"prompt":"go","providers":{"on_limit":"fallback"}}`,
		"not an object":    `{"prompt":"go","providers":"codex"}`,
	} {
		_, err := parseSpawnToolInput(in)
		assert.Error(t, err, name)
	}
}

type ProviderPolicySuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestProviderPolicyWorkflow(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(ProviderPolicySuite))
}

// runSpawning runs an agent that spawns one sub-agent (with spawnArgs added to
// the call) and returns the Failover policy each call_llm turn was handed,
// root and child.
func (s *ProviderPolicySuite) runSpawning(chatID string, inputs, spawnArgs map[string]interface{}) (root, sub []types.FailoverPolicy) {
	env := s.NewTestWorkflowEnvironment()
	call := spawnToolCall("tc1", "scan the repo")
	if spawnArgs != nil {
		in := map[string]interface{}{"preset": "general", "prompt": "scan the repo"}
		for k, v := range spawnArgs {
			in[k] = v
		}
		data, _ := json.Marshal(in)
		call["input"] = string(data)
	}
	e := newSpawnE2EEnv(s.T(), env, []scriptedToolCallsResponse{{toolCalls: []map[string]interface{}{call}}})
	input := spawnE2EWorkflowInput(chatID)
	input.Inputs = inputs
	env.ExecuteWorkflow(DynamicWorkflow, input)
	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	rootThread := "thread-" + chatID
	for _, rtx := range e.callLLMRuntimes {
		if rtx.Thread == rootThread {
			root = append(root, rtx.Failover)
		} else {
			sub = append(sub, rtx.Failover)
		}
	}
	s.Require().NotEmpty(root)
	s.Require().NotEmpty(sub, "the sub-agent must have taken a turn")
	return root, sub
}

// The chat opted in to a fallback; the sub-agent it spawns follows the same
// policy, with no YAML forwarding it.
func (s *ProviderPolicySuite) TestSubAgentInheritsTheChatsPolicy() {
	root, sub := s.runSpawning("chat-policy", map[string]interface{}{
		"model":                "claude-5.5-opus@claude",
		InputKeyProviderPolicy: policyOf("fallback", "gpt-5.6-sol@codex"),
	}, nil)
	for _, p := range append(root, sub...) {
		s.Equal(types.FailoverPolicy{FallbackModel: "gpt-5.6-sol@codex"}, p)
	}
}

// A spawn call's providers beats what the chat set, for that sub-agent alone.
func (s *ProviderPolicySuite) TestSpawnProvidersBeatTheChatsPolicy() {
	root, sub := s.runSpawning("chat-spawn-override", map[string]interface{}{
		"model":                "claude-5.5-opus@claude",
		InputKeyProviderPolicy: policyOf("fallback", "gpt-5.6-sol@codex"),
	}, map[string]interface{}{"providers": map[string]interface{}{"on_limit": "wait"}})
	for _, p := range root {
		s.Equal("gpt-5.6-sol@codex", p.FallbackModel, "the root keeps the chat's policy")
	}
	for _, p := range sub {
		s.Equal(types.FailoverPolicy{}, p, "the sub-agent was told to wait")
	}
}

// A chat with no policy keeps no policy anywhere: nothing is invented.
func (s *ProviderPolicySuite) TestNoPolicyStaysNone() {
	root, sub := s.runSpawning("chat-nopolicy", map[string]interface{}{"model": "claude-5.5-opus@claude"}, nil)
	for _, p := range append(root, sub...) {
		s.Equal(types.FailoverPolicy{}, p)
	}
}
