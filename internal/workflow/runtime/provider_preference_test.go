// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow"
)

func TestProviderPreference(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		inputs map[string]interface{}
		want   []string
	}{
		"nil inputs":                                   {nil, nil},
		"no model, nothing inherited":                  {map[string]interface{}{}, nil},
		"pinned model id":                              {map[string]interface{}{"model": "gpt-5.6-sol@codex"}, []string{"codex"}},
		"model object with pinned id":                  {map[string]interface{}{"model": map[string]interface{}{"id": "gpt-5.6-sol@codex", "tags": []interface{}{"flagship"}}}, []string{"codex"}},
		"model object with providers":                  {map[string]interface{}{"model": map[string]interface{}{"tags": []interface{}{"fast"}, "providers": []interface{}{"openai", "codex"}}}, []string{"openai", "codex"}},
		"tier-only model inherits":                     {map[string]interface{}{"model": map[string]interface{}{"tags": []interface{}{"fast"}}, InputKeyProviderPreference: []string{"codex"}}, []string{"codex"}},
		"own pin beats inherited":                      {map[string]interface{}{"model": "claude-x@anthropic", InputKeyProviderPreference: []string{"codex"}}, []string{"anthropic"}},
		"subscription pin is a registry provider":      {map[string]interface{}{"model": "claude-5.5-opus@claude"}, []string{"claude"}},
		"subscription and key are distinct":            {map[string]interface{}{"model": map[string]interface{}{"tags": []interface{}{"fast"}, "providers": []interface{}{"claude", "anthropic"}}}, []string{"claude", "anthropic"}},
		"inherited subscription preference is carried": {map[string]interface{}{"model": map[string]interface{}{"tags": []interface{}{"fast"}}, InputKeyProviderPreference: []interface{}{"claude"}}, []string{"claude"}},
		"local models are not carried": {
			map[string]interface{}{"model": "qwen3:latest@local", InputKeyProviderPreference: []interface{}{"local:gpu", "endpoint:e1"}}, nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ProviderPreference(tc.inputs))
		})
	}
}

func TestPropagateProviderPreference(t *testing.T) {
	t.Parallel()
	t.Run("a pinned parent hands its provider to the child", func(t *testing.T) {
		child := map[string]interface{}{"model": map[string]interface{}{"tags": []interface{}{"fast"}}}
		propagateProviderPreference(map[string]interface{}{"model": "gpt-5.6-sol@codex"}, child)
		assert.Equal(t, []string{"codex"}, child[InputKeyProviderPreference])
	})
	t.Run("a child's own preference (preset or spawn args) beats the inherited one", func(t *testing.T) {
		child := map[string]interface{}{InputKeyProviderPreference: []interface{}{"anthropic"}}
		propagateProviderPreference(map[string]interface{}{"model": "gpt-5.6-sol@codex"}, child)
		assert.Equal(t, []string{"anthropic"}, child[InputKeyProviderPreference])
	})
	t.Run("a parent with no preference adds nothing", func(t *testing.T) {
		child := map[string]interface{}{}
		propagateProviderPreference(map[string]interface{}{"model": map[string]interface{}{"tags": []interface{}{"fast"}}}, child)
		assert.NotContains(t, child, InputKeyProviderPreference)
	})
	t.Run("nil child is a no-op", func(t *testing.T) {
		assert.NotPanics(t, func() { propagateProviderPreference(map[string]interface{}{"model": "m@codex"}, nil) })
	})
}

func TestProviderPreferenceIsRuntimeInjected(t *testing.T) {
	t.Parallel()
	assert.True(t, workflow.RuntimeInjectedInputs[InputKeyProviderPreference],
		"a client must not be able to set the run's provider preference")
}

// The inheritance of a spawned child happens where its preset and args are
// already in place (the inline executor), so buildSpawnChildInputs must not
// pre-fill it: an inherited value in the child's node args would shadow the
// child's preset `providers:`.
func TestBuildSpawnChildInputsLeavesProviderInheritanceToTheExecutor(t *testing.T) {
	t.Parallel()
	child := buildSpawnChildInputs(map[string]interface{}{"mode": "auto", "model": "gpt-5.6-sol@codex"})
	assert.NotContains(t, child, InputKeyProviderPreference)
	assert.NotContains(t, child, InputKeyProviderPolicy)
	assert.NotContains(t, child, "model", "the child's model still comes from its preset, never the parent's")
}

type ProviderPreferenceSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestProviderPreferenceWorkflow(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(ProviderPreferenceSuite))
}

// runSpawningAgent runs an agent that spawns one sub-agent and returns the
// ProviderPreference each call_llm turn of the run was handed, root and child.
func (s *ProviderPreferenceSuite) runSpawningAgent(chatID string, inputs map[string]interface{}) (root, sub [][]string) {
	env := s.NewTestWorkflowEnvironment()
	e := newSpawnE2EEnv(s.T(), env, []scriptedToolCallsResponse{
		{toolCalls: []map[string]interface{}{spawnToolCall("tc1", "scan the repo")}},
	})
	input := spawnE2EWorkflowInput(chatID)
	input.Inputs = inputs
	env.ExecuteWorkflow(DynamicWorkflow, input)
	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	rootThread := "thread-" + chatID
	for _, rtx := range e.callLLMRuntimes {
		if rtx.Thread == rootThread {
			root = append(root, rtx.ProviderPreference)
		} else {
			sub = append(sub, rtx.ProviderPreference)
		}
	}
	s.Require().Positive(subAgentTurns(e, rootThread), "the sub-agent must have taken a turn")
	return root, sub
}

// A chat the user put on a codex model: the root's call_llm and the spawned
// sub-agent's both carry codex as the provider to resolve a tier on, although
// the sub-agent's preset names only a tier. (handlers tests prove that a tier
// selector carrying codex resolves to codex with Claude connected.)
func (s *ProviderPreferenceSuite) TestSubAgentOfAPinnedChatInheritsTheChatsProvider() {
	root, sub := s.runSpawningAgent("chat-codex", map[string]interface{}{"model": "gpt-5.6-sol@codex"})
	s.Require().NotEmpty(root)
	s.Require().NotEmpty(sub)
	for _, p := range append(root, sub...) {
		s.Equal([]string{"codex"}, p)
	}
}

// A chat on a tier alone has no provider to carry: sub-agents resolve as before.
func (s *ProviderPreferenceSuite) TestTierOnlyChatCarriesNoPreference() {
	root, sub := s.runSpawningAgent("chat-tier", map[string]interface{}{
		"model": map[string]interface{}{"tags": []interface{}{"flagship"}},
	})
	s.Require().NotEmpty(sub)
	for _, p := range append(root, sub...) {
		s.Empty(p)
	}
}

// The preference is the RUN's own pin, which wins over what the run inherited:
// a run explicitly on anthropic hands its sub-agents anthropic, not the codex
// it was itself given.
func (s *ProviderPreferenceSuite) TestOwnPinBeatsInheritedPreferenceForChildren() {
	root, sub := s.runSpawningAgent("chat-own-pin", map[string]interface{}{
		"model":                    "claude-5.5-sonnet@anthropic",
		InputKeyProviderPreference: []string{"codex"},
	})
	s.Require().NotEmpty(root)
	s.Require().NotEmpty(sub)
	for _, p := range append(root, sub...) {
		s.Equal([]string{"anthropic"}, p)
	}
}
