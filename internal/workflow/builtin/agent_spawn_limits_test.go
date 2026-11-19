// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The agent workflow declares the sub-agent concurrency caps as inputs so a
// run can set them; their defaults must be the runtime's, or the UI and the
// docs would name one limit while the runtime enforces another.
func TestAgentWorkflow_SpawnLimitInputsMatchTheRuntimeDefaults(t *testing.T) {
	t.Parallel()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
	require.NoError(t, err)

	var wf struct {
		Inputs map[string]struct {
			Type    string      `yaml:"type"`
			Default interface{} `yaml:"default"`
		} `yaml:"inputs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &wf))

	for name, want := range map[string]int{
		types.MaxConcurrentSubAgentsInput:      types.DefaultMaxConcurrentSubAgents,
		types.MaxConcurrentSubAgentsTotalInput: types.DefaultMaxConcurrentSubAgentsTotal,
	} {
		in, ok := wf.Inputs[name]
		require.True(t, ok, "agent.yaml declares %s", name)
		assert.Equal(t, "integer", in.Type, name)
		assert.Equal(t, want, in.Default, name)
	}
}
