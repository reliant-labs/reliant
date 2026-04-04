package simulator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const workflowWithParallelLoop = `
name: test-parallel-loop
apiVersion: "1.0"
entry: [implement_all]
inputs:
  components:
    type: array
  component_map:
    type: object
nodes:
  - id: implement_all
    type: loop
    parallel: true
    items: "{{inputs.components}}"
    key: "{{iter.item.name}}"
    ref: builtin://agent
    args:
      prompt: "Implement {{iter.item.name}} from {{iter.item.spec}}"
  - id: map_all
    type: loop
    parallel: true
    items: "{{inputs.component_map}}"
    ref: builtin://agent
    args:
      prompt: "Implement {{iter.key}} from {{iter.item.spec}}"
edges:
  - from: implement_all
    default: map_all
`

func TestEngine_RunScenario_ParallelLoopOutputs(t *testing.T) {
	engine, err := NewEngineFromYAML(workflowWithParallelLoop)
	require.NoError(t, err)

	t.Run("parallel loop uses key expression and exposes aggregate output shape", func(t *testing.T) {
		scenario := &Scenario{
			Name: "parallel_loop_keyed_results",
			Inputs: map[string]interface{}{
				"components": []interface{}{
					map[string]interface{}{"name": "auth", "spec": "login flow"},
					map[string]interface{}{"name": "billing", "spec": "invoice flow"},
				},
				"component_map": map[string]interface{}{},
			},
			Events: []SimulatedEvent{
				{Node: "builtin://agent", Output: map[string]interface{}{"response_text": "auth complete", "component": "auth"}},
				{Node: "builtin://agent", Output: map[string]interface{}{"response_text": "billing complete", "component": "billing"}},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				NodeOutputs: map[string]map[string]interface{}{
					"implement_all": {
						"_iterations": 2,
						"_completed":  2,
						"_failed":     0,
						"_parallel":   true,
						"_results": map[string]interface{}{
							"auth":    map[string]interface{}{"response_text": "auth complete", "component": "auth"},
							"billing": map[string]interface{}{"response_text": "billing complete", "component": "billing"},
						},
					},
					"map_all": {
						"_iterations": 0,
						"_completed":  0,
						"_failed":     0,
						"_parallel":   true,
						"_results":    map[string]interface{}{},
					},
				},
			},
		}

		result := engine.RunScenario(scenario)
		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	})

	t.Run("parallel loop over map defaults result keys to source map keys", func(t *testing.T) {
		scenario := &Scenario{
			Name: "parallel_loop_map_keys",
			Inputs: map[string]interface{}{
				"components": []interface{}{},
				"component_map": map[string]interface{}{
					"auth":    map[string]interface{}{"spec": "login flow"},
					"billing": map[string]interface{}{"spec": "invoice flow"},
				},
			},
			Events: []SimulatedEvent{
				{Node: "builtin://agent", Output: map[string]interface{}{"response_text": "auth complete", "component": "auth"}},
				{Node: "builtin://agent", Output: map[string]interface{}{"response_text": "billing complete", "component": "billing"}},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				NodeOutputs: map[string]map[string]interface{}{
					"implement_all": {
						"_iterations": 0,
						"_completed":  0,
						"_failed":     0,
						"_parallel":   true,
						"_results":    map[string]interface{}{},
					},
					"map_all": {
						"_iterations": 2,
						"_completed":  2,
						"_failed":     0,
						"_parallel":   true,
						"_results": map[string]interface{}{
							"auth":    map[string]interface{}{"response_text": "auth complete", "component": "auth"},
							"billing": map[string]interface{}{"response_text": "billing complete", "component": "billing"},
						},
					},
				},
			},
		}

		result := engine.RunScenario(scenario)
		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	})
}
